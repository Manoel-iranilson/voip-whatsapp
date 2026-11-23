package main

import (
	"time"

	"wacalls/internal/voip/call"
	"wacalls/internal/voip/core"
)

// A call leaves the registry only when WhatsApp signals its end, the API ends
// it, or its duration cap fires. A terminate stanza that never arrives (or that
// arrives for a state the manager ignores) used to leave the call registered
// forever, so stale entries piled up until every new call got "max concurrent
// calls". The reaper ends anything that has clearly stopped being a live call.
const (
	// reapInterval is short because the reaper also stands in for the peer's
	// hang-up (see peerGoneTimeout), and that has to show up on screen quickly.
	reapInterval = 2 * time.Second
	// orphanTimeout covers an entry whose manager never got a call, such as an
	// offer that could not be parsed.
	orphanTimeout = 30 * time.Second
	// ringTimeout is longer than WhatsApp's own ring timeout, so the normal
	// path (the peer's terminate) always gets to end the call first.
	ringTimeout = 90 * time.Second
	// connectTimeout is how long an accepted call may go without any peer
	// audio before the media path is considered failed.
	connectTimeout = 60 * time.Second
	// mediaIdleTimeout ends an established call that has received no audio
	// packet at all for this long. Peers send packets during silence too, so a
	// call this quiet has lost its media path or missed its terminate.
	mediaIdleTimeout = 3 * time.Minute
	// peerGoneTimeout ends an established call once the peer, after having
	// sent audio, sends nothing at all for this long. Since mid-November 2026
	// no call stanza from the peer reaches us on outbound calls — neither the
	// accept nor the terminate — so the peer hanging up is only visible as
	// their audio stopping. Silence alone does not trigger it: a silent peer
	// still sends packets, only less often.
	peerGoneTimeout = 5 * time.Second
)

// staleVerdict says what the reaper should do with one registered call.
type staleVerdict struct {
	stale bool
	// ended means the manager already ended the call and only the registry
	// entry is left; there is nothing to send to WhatsApp.
	ended  bool
	reason core.EndCallReason
	why    string
}

// judgeCall decides whether a registered call is stale. It is pure so that the
// timeouts can be tested without a WhatsApp connection.
func judgeCall(snap call.CallSnapshot, hasCall bool, rc registeredCall, now time.Time) staleVerdict {
	if !hasCall {
		if now.Sub(rc.addedAt) > orphanTimeout {
			return staleVerdict{stale: true, reason: core.EndCallReasonFailed, why: "call never started"}
		}
		return staleVerdict{}
	}
	switch snap.State {
	case core.CallStateEnded:
		return staleVerdict{stale: true, ended: true, reason: core.EndCallReasonUnknown, why: "ended but still registered"}
	case core.CallStateInitiating, core.CallStateRinging, core.CallStateIncomingRinging:
		// Peer audio counts as life here too. A call whose accept was never
		// applied stays "ringing" while two people are talking on it, and
		// ending it on the ring timeout would hang up a live conversation.
		if now.Sub(lastSign(snap, rc)) > ringTimeout {
			return staleVerdict{stale: true, reason: core.EndCallReasonTimeout, why: "rang too long without an answer"}
		}
	case core.CallStateConnecting:
		if now.Sub(lastSign(snap, rc)) > connectTimeout {
			return staleVerdict{stale: true, reason: core.EndCallReasonFailed, why: "accepted but media never flowed"}
		}
	default: // active, on hold
		if !rc.lastPeerAudio.IsZero() && now.Sub(rc.lastPeerAudio) > peerGoneTimeout {
			return staleVerdict{stale: true, reason: core.EndCallReasonUserEnded, why: "peer audio stopped; assuming they hung up"}
		}
		if now.Sub(lastSign(snap, rc)) > mediaIdleTimeout {
			return staleVerdict{stale: true, reason: core.EndCallReasonFailed, why: "no peer audio for too long"}
		}
	}
	return staleVerdict{}
}

// lastSign is the most recent evidence that the call was alive: peer audio,
// or failing that the moment it was accepted or created.
func lastSign(snap call.CallSnapshot, rc registeredCall) time.Time {
	last := snap.CreatedAt
	if snap.AcceptedAt != nil && snap.AcceptedAt.After(last) {
		last = *snap.AcceptedAt
	}
	if rc.lastPeerAudio.After(last) {
		last = rc.lastPeerAudio
	}
	return last
}

func (s *Session) runReaper(stop <-chan struct{}) {
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			s.reapStaleCalls(now)
		}
	}
}

func (s *Session) reapStaleCalls(now time.Time) {
	for _, rc := range s.reg.list() {
		if rc.ac.cm == nil {
			continue
		}
		snap, ok := rc.ac.cm.Snapshot()
		v := judgeCall(snap, ok, rc, now)
		if !v.stale {
			continue
		}
		s.log.Warn("reaping stale call", "call_id", rc.id, "state", string(snap.State),
			"why", v.why, "age", now.Sub(rc.addedAt).Round(time.Second))
		if !v.ended {
			s.terminateCall(rc.id, v.reason)
		}
		s.removeCall(rc.id)
		s.mgr.broker.endCall(rc.id, string(v.reason))
	}
}

// logCallsAtCapacity lists what is holding the session's call slots, so a
// "max concurrent calls" refusal can be explained from the logs.
func (s *Session) logCallsAtCapacity() {
	now := time.Now()
	for _, rc := range s.reg.list() {
		var snap call.CallSnapshot
		if rc.ac.cm != nil {
			snap, _ = rc.ac.cm.Snapshot()
		}
		s.log.Warn("call slot in use", "call_id", rc.id, "state", string(snap.State),
			"age", now.Sub(rc.addedAt).Round(time.Second))
	}
}
