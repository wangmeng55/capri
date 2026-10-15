package ochost

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/AgentsHarness/capri-transport/procattr"
)

// Config configures the opencode serve supervisor.
type Config struct {
	// BinPath is the opencode executable; empty triggers discovery.
	BinPath string
	// Cwd is the working directory for opencode (empty = inherit).
	Cwd string
	// PrivatePort is the loopback port for opencode serve (0 = pick a free one).
	PrivatePort int
	// Username/Password are the local Basic credentials injected by LocalProxy.
	Username string
	Password string
	// StartupDeadline bounds the wait for /global/health (default 30s).
	StartupDeadline time.Duration
}

// Supervisor owns one opencode serve child process and its health state. It is
// NOT the same OS process as opencode-host (C4 §7).
type Supervisor struct {
	cfg  Config
	base string

	mu      sync.Mutex
	cmd     *exec.Cmd
	waitErr error
	exited  bool
	ready   bool
	lastErr string
}

// NewSupervisor returns a supervisor for cfg.
func NewSupervisor(cfg Config) *Supervisor {
	if cfg.StartupDeadline <= 0 {
		cfg.StartupDeadline = 30 * time.Second
	}
	if cfg.Username == "" {
		cfg.Username = "opencode"
	}
	return &Supervisor{cfg: cfg}
}

// Start resolves the binary, picks a port, spawns `opencode serve`, and waits
// for /global/health. On any failure the child is reaped before returning (no
// unmanaged orphan, per C4).
func (s *Supervisor) Start(ctx context.Context) error {
	bin := DiscoverOpencodeBin(s.cfg.BinPath)
	if bin == "" {
		return fmt.Errorf("ochost: opencode executable not found (set OPENCODE_BIN or --opencode-bin)")
	}
	port := s.cfg.PrivatePort
	if port <= 0 {
		p, err := freeLoopbackPort()
		if err != nil {
			return err
		}
		port = p
	}
	s.base = fmt.Sprintf("http://127.0.0.1:%d", port)

	args := []string{"serve", "--port", fmt.Sprint(port), "--hostname", "127.0.0.1"}
	cmd := exec.Command(bin, args...)
	if s.cfg.Cwd != "" {
		cmd.Dir = s.cfg.Cwd
	}
	cmd.Env = setEnv(os.Environ(), "OPENCODE_SERVER_PASSWORD", s.cfg.Password)
	procattr.HideConsole(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ochost: start opencode serve: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.ready = false
	s.lastErr = ""
	s.mu.Unlock()

	// Reap on exit so a crash flips ready and is observable.
	go func() {
		werr := cmd.Wait()
		s.mu.Lock()
		s.waitErr = werr
		s.exited = true
		s.ready = false
		if werr != nil {
			s.lastErr = werr.Error()
		}
		s.mu.Unlock()
	}()

	if err := s.waitHealthy(ctx, s.cfg.StartupDeadline); err != nil {
		s.kill()
		return err
	}
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) waitHealthy(ctx context.Context, deadline time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	url := s.base + "/global/health"
	for {
		if s.childExited() {
			s.mu.Lock()
			le := s.lastErr
			s.mu.Unlock()
			return fmt.Errorf("ochost: opencode exited during startup (%s)", le)
		}
		if err := s.probe(ctx, url); err == nil {
			return nil
		} else {
			s.mu.Lock()
			s.lastErr = err.Error()
			s.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			s.mu.Lock()
			le := s.lastErr
			s.mu.Unlock()
			return fmt.Errorf("ochost: opencode not healthy within %s: %w (last: %s)", deadline, ctx.Err(), le)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Supervisor) childExited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

func (s *Supervisor) probe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if s.cfg.Password != "" || s.cfg.Username != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(s.cfg.Username+":"+s.cfg.Password)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

// Stop terminates the child and waits for it to exit.
func (s *Supervisor) Stop() { s.kill() }

func (s *Supervisor) kill() {
	s.mu.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.ready = false
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	// Wait is already running; give it a moment to reap.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		done := s.waitErr != nil || cmd.ProcessState != nil
		s.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Ready reports whether the server passed its health check and has not exited.
func (s *Supervisor) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

// BaseURL is the loopback base once Start has succeeded.
func (s *Supervisor) BaseURL() string { return s.base }

// LastError is the most recent startup/health failure, empty when healthy.
func (s *Supervisor) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// setEnv returns env with key set to val, replacing any existing entry (a bare
// append would leave two entries and let an inherited value win on Windows).
// An empty val removes the key.
func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	if val != "" {
		out = append(out, prefix+val)
	}
	return out
}
