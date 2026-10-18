package transport

import "sync"

// fakeSource is a minimal EventSource test double for the shared client.
//
// Subscribe returns a buffered channel registered as a subscriber; Broadcast
// delivers to every current subscriber (like the real Bridge's fan-out). The
// host identity is not read by the client (it comes from Config), so only the
// event stream and the status snapshot are modeled.
type fakeSource struct {
	mu     sync.Mutex
	st     Status
	subs   map[chan Event]struct{}
	closed bool
}

func newFakeSource(hostID, hostName string) *fakeSource {
	return &fakeSource{subs: make(map[chan Event]struct{})}
}

func (f *fakeSource) Subscribe() (chan Event, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan Event, 256)
	f.subs[ch] = struct{}{}
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.subs[ch]; ok {
			delete(f.subs, ch)
			close(ch)
		}
	}
}

func (f *fakeSource) Snapshot() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

// Broadcast pushes one event to every current subscriber.
func (f *fakeSource) Broadcast(ev Event) {
	f.mu.Lock()
	subs := make([]chan Event, 0, len(f.subs))
	for ch := range f.subs {
		subs = append(subs, ch)
	}
	f.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// SetHostName satisfies transport.NameSetter (the client may assert for it).
func (f *fakeSource) SetHostName(string) {}

// Shutdown satisfies tests that defer bridge.Shutdown() on the real Bridge.
func (f *fakeSource) Shutdown() {}
