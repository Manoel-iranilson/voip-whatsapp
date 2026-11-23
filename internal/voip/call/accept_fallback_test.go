package call

import (
	"log/slog"
	"testing"

	"wacalls/internal/voip/core"
)

func TestRemoteAcceptedFromInitiating(t *testing.T) {
	c := NewOutgoingCall("c1", "peer@s.whatsapp.net", "me@s.whatsapp.net", core.CallMediaTypeAudio)
	if err := c.ApplyTransition(Transition{Type: TransitionRemoteAccepted}); err != nil {
		t.Fatalf("accept before offer ack must apply: %v", err)
	}
	if c.StateData.State != core.CallStateConnecting {
		t.Fatalf("state = %s, want connecting", c.StateData.State)
	}
}

func voiced() []float32 {
	f := make([]float32, 320)
	for i := range f {
		if i%2 == 0 {
			f[i] = 0.1
		} else {
			f[i] = -0.1
		}
	}
	return f
}

func TestPromoteOnPeerVoice(t *testing.T) {
	m := &CallManager{log: slog.Default()}
	m.currentCall = NewOutgoingCall("c1", "peer@s.whatsapp.net", "me@s.whatsapp.net", core.CallMediaTypeAudio)
	_ = m.currentCall.ApplyTransition(Transition{Type: TransitionOfferSent})
	var emitted []core.CallState
	m.OnStateChange = func(c *CallInfo) { emitted = append(emitted, c.StateData.State) }

	silence := make([]float32, 320)
	for i := 0; i < 50; i++ {
		m.promoteOnPeerVoice(silence)
	}
	if m.currentCall.StateData.State != core.CallStateRinging {
		t.Fatalf("silence must not answer the call, state = %s", m.currentCall.StateData.State)
	}

	for i := 0; i < peerVoiceFrames; i++ {
		m.promoteOnPeerVoice(voiced())
	}
	if m.currentCall.StateData.State != core.CallStateActive {
		t.Fatalf("voice must mark the call active, state = %s", m.currentCall.StateData.State)
	}
	if len(emitted) != 1 || emitted[0] != core.CallStateActive {
		t.Fatalf("emitted = %v, want one active", emitted)
	}
}

func TestPromoteOnPeerVoiceIgnoresIncoming(t *testing.T) {
	m := &CallManager{log: slog.Default()}
	m.currentCall = NewIncomingCall("c1", "peer@s.whatsapp.net", "peer@s.whatsapp.net", "", core.CallMediaTypeAudio)
	for i := 0; i < 20; i++ {
		m.promoteOnPeerVoice(voiced())
	}
	if m.currentCall.StateData.State != core.CallStateIncomingRinging {
		t.Fatalf("an unanswered incoming call must stay ringing, state = %s", m.currentCall.StateData.State)
	}
}
