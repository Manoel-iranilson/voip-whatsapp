package main

import (
	"sync"
	"time"

	"wacalls/internal/voip/call"
)

type activeCall struct {
	cm     *call.CallManager
	bridge *Bridge
	// rec is nil when this call is not being recorded; every callRecorder method
	// tolerates that, so the audio paths call it without checking.
	rec *callRecorder
	// cap ends the call when duration_ms was given. It has to be stopped when the
	// call ends on its own, or it fires against a call id that is gone.
	cap *time.Timer
	// addedAt and lastPeerAudio let the reaper find calls whose end was never
	// signalled. lastPeerAudio is guarded by the registry lock.
	addedAt       time.Time
	lastPeerAudio time.Time
}

// registeredCall pairs a call id with its entry, for scans over the registry.
type registeredCall struct {
	id            string
	ac            *activeCall
	addedAt       time.Time
	lastPeerAudio time.Time
}

type callRegistry struct {
	mu    sync.Mutex
	calls map[string]*activeCall
}

func newCallRegistry() *callRegistry {
	return &callRegistry{calls: map[string]*activeCall{}}
}

func (r *callRegistry) add(callID string, ac *activeCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ac.addedAt.IsZero() {
		ac.addedAt = time.Now()
	}
	r.calls[callID] = ac
}

// touchPeerAudio records that audio from the peer just arrived on callID.
func (r *callRegistry) touchPeerAudio(callID string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ac, ok := r.calls[callID]; ok {
		ac.lastPeerAudio = at
	}
}

// list returns every registered call. The entries are copies of the timing
// fields, so the caller can inspect them without holding the lock.
func (r *callRegistry) list() []registeredCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]registeredCall, 0, len(r.calls))
	for id, ac := range r.calls {
		out = append(out, registeredCall{id: id, ac: ac, addedAt: ac.addedAt, lastPeerAudio: ac.lastPeerAudio})
	}
	return out
}

func (r *callRegistry) get(callID string) (*activeCall, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	return ac, ok
}

func (r *callRegistry) remove(callID string) (*activeCall, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	if !ok {
		return nil, false
	}
	delete(r.calls, callID)
	return ac, true
}

func (r *callRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *callRegistry) setCap(callID string, t *time.Timer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	if !ok {
		return false
	}
	if ac.cap != nil {
		ac.cap.Stop()
	}
	ac.cap = t
	return true
}

func (r *callRegistry) setBridge(callID string, b *Bridge) (*Bridge, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	if !ok {
		return nil, false
	}
	oldB := ac.bridge
	ac.bridge = b
	return oldB, true
}

func (r *callRegistry) drain() []*activeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*activeCall, 0, len(r.calls))
	for _, ac := range r.calls {
		out = append(out, ac)
	}
	r.calls = map[string]*activeCall{}
	return out
}
