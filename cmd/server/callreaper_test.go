package main

import (
	"testing"
	"time"

	"wacalls/internal/voip/call"
	"wacalls/internal/voip/core"
)

func TestJudgeCall(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	at := func(d time.Duration) *time.Time { v := ago(d); return &v }

	cases := []struct {
		name    string
		snap    call.CallSnapshot
		hasCall bool
		rc      registeredCall
		stale   bool
		ended   bool
	}{
		{"fresh orphan kept", call.CallSnapshot{}, false, registeredCall{addedAt: ago(10 * time.Second)}, false, false},
		{"old orphan reaped", call.CallSnapshot{}, false, registeredCall{addedAt: ago(time.Minute)}, true, false},
		{"ended entry removed", call.CallSnapshot{State: core.CallStateEnded, CreatedAt: ago(time.Second)}, true, registeredCall{}, true, true},
		{"ringing within timeout", call.CallSnapshot{State: core.CallStateRinging, CreatedAt: ago(30 * time.Second)}, true, registeredCall{}, false, false},
		{"ringing too long", call.CallSnapshot{State: core.CallStateRinging, CreatedAt: ago(2 * time.Minute)}, true, registeredCall{}, true, false},
		{"ringing but peer audio flowing kept", call.CallSnapshot{State: core.CallStateRinging, CreatedAt: ago(5 * time.Minute)}, true, registeredCall{lastPeerAudio: ago(2 * time.Second)}, false, false},
		{"incoming ringing too long", call.CallSnapshot{State: core.CallStateIncomingRinging, CreatedAt: ago(2 * time.Minute)}, true, registeredCall{}, true, false},
		{"connecting with no audio", call.CallSnapshot{State: core.CallStateConnecting, CreatedAt: ago(3 * time.Minute), AcceptedAt: at(2 * time.Minute)}, true, registeredCall{}, true, false},
		// The "stuck on calling" bug: state never reaches active, yet audio flows.
		// Such a call is live and must not be cut.
		{"connecting with live audio kept", call.CallSnapshot{State: core.CallStateConnecting, CreatedAt: ago(10 * time.Minute), AcceptedAt: at(9 * time.Minute)}, true, registeredCall{lastPeerAudio: ago(time.Second)}, false, false},
		{"active long call with audio kept", call.CallSnapshot{State: core.CallStateActive, CreatedAt: ago(time.Hour), AcceptedAt: at(time.Hour)}, true, registeredCall{lastPeerAudio: ago(2 * time.Second)}, false, false},
		{"active with audio gone", call.CallSnapshot{State: core.CallStateActive, CreatedAt: ago(time.Hour), AcceptedAt: at(time.Hour)}, true, registeredCall{lastPeerAudio: ago(5 * time.Minute)}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := judgeCall(tc.snap, tc.hasCall, tc.rc, now)
			if v.stale != tc.stale || v.ended != tc.ended {
				t.Fatalf("got stale=%v ended=%v (%s), want stale=%v ended=%v", v.stale, v.ended, v.why, tc.stale, tc.ended)
			}
		})
	}
}

func TestRegistryListAndTouch(t *testing.T) {
	r := newCallRegistry()
	r.add("a", &activeCall{})
	when := time.Now()
	r.touchPeerAudio("a", when)
	r.touchPeerAudio("missing", when) // must not panic or create an entry
	got := r.list()
	if len(got) != 1 || got[0].id != "a" || !got[0].lastPeerAudio.Equal(when) || got[0].addedAt.IsZero() {
		t.Fatalf("unexpected list: %+v", got)
	}
}
