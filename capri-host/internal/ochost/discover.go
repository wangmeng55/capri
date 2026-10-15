// Package ochost implements the opencode-host side of the capri remote-access
// feature: discovering and supervising a local `opencode serve`, and forwarding
// transparent tunnel requests to it with the host-local Basic credential
// injected. It contains no opencode API knowledge beyond the fixed loopback
// base and the health path (C1/C3): request endpoints are never enumerated.
package ochost

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DiscoverOpencodeBin locates the opencode executable: an explicit path wins,
// then $OPENCODE_BIN, then PATH, then common install locations. Returns "" when
// nothing is found (the caller reports it, per the design's fail-fast rule).
func DiscoverOpencodeBin(explicit string) string {
	if p := strings.TrimSpace(explicit); p != "" {
		if fileExists(p) {
			return p
		}
		return ""
	}
	if p := strings.TrimSpace(os.Getenv("OPENCODE_BIN")); p != "" {
		if fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath("opencode"); err == nil && fileExists(p) {
		return p
	}
	for _, c := range candidatePaths() {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func candidatePaths() []string {
	home, _ := os.UserHomeDir()
	var out []string
	if runtime.GOOS == "windows" {
		exe := "opencode.exe"
		if home != "" {
			out = append(out,
				filepath.Join(home, ".opencode", "bin", exe),
				filepath.Join(home, "AppData", "Local", "Programs", "opencode", exe),
			)
		}
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			out = append(out, filepath.Join(la, "Programs", "opencode", exe))
		}
		out = append(out, `D:\opencode\bin\`+exe, `C:\opencode\bin\`+exe)
	} else {
		if home != "" {
			out = append(out,
				filepath.Join(home, ".opencode", "bin", "opencode"),
				filepath.Join(home, ".local", "bin", "opencode"),
				filepath.Join(home, ".bun", "bin", "opencode"),
			)
		}
		out = append(out, "/usr/local/bin/opencode", "/opt/homebrew/bin/opencode")
	}
	return out
}
