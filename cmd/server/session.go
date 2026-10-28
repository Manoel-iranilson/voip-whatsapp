package main

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"wacalls/internal/voip/call"
	"wacalls/internal/voip/core"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/wanode"
	"wacalls/internal/wa"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Session struct {
	id   string
	name string
	mgr  *SessionManager
	log  *slog.Logger

	client *whatsmeow.Client
	reg    *callRegistry

	mu   sync.Mutex
	auth AuthSnapshot
}

func newSession(mgr *SessionManager, id, name string, client *whatsmeow.Client) *Session {
	s := &Session{
		id:     id,
		name:   name,
		mgr:    mgr,
		log:    mgr.log.With("session", id),
		client: client,
		auth:   AuthSnapshot{State: "connecting"},
		reg:    newCallRegistry(),
	}
	client.AddEventHandler(s.handleEvent)
	return s
}

// createCall registers a new call. When record is true and a recording directory
// is configured, both legs of its audio are written to disk; a recorder that
// fails to open is logged and the call proceeds without one, because losing the
// recording is better than losing the call.
func (s *Session) createCall(callID string, record bool) *call.CallManager {
	cm := call.NewCallManager(wa.NewSocket(s.client), s.log)
	s.wireCall(cm, callID)

	var rec *callRecorder
	if record {
		var err error
		rec, err = newCallRecorder(s.mgr.recordDir, s.id, callID, s.log)
		if err != nil {
			s.log.Error("could not start recording; call continues unrecorded", "call_id", callID, "err", err)
		}
	}

	s.reg.add(callID, &activeCall{cm: cm, rec: rec})
	return cm
}

// shouldRecord decides whether a call gets recorded. Recording has to be enabled
// server-side at all; beyond that -record-all covers every call, including
// inbound ones, which have no per-call flag to carry the request.
func (s *Session) shouldRecord(requested bool) bool {
	if s.mgr.recordDir == "" {
		return false
	}
	return requested || s.mgr.recordAll
}

// recordingBase is the recording file stem of a live call, empty when it is not
// being recorded. It has to be stamped onto the broker record while the call is
// still running: by the time history is written the registry entry is already
// gone, because OnStateChange removes the call before ending it.
func (s *Session) recordingBase(callID string) string {
	ac, ok := s.reg.get(callID)
	if !ok {
		return ""
	}
	return ac.rec.Base()
}

func (s *Session) wireCall(cm *call.CallManager, callID string) {
	cm.OnIncoming = func(c *call.CallInfo) {
		s.mgr.broker.upsertCall(CallRecord{
			SessionID: s.id, CallID: c.CallID, Direction: "inbound", Peer: c.PeerJid,
			StartedAt: time.Now().UnixMilli(), Status: StatusRinging,
			Recording: s.recordingBase(c.CallID),
		})
		s.mgr.broker.emitIncoming(s.id, c.CallID, c.PeerJid)
	}
	cm.OnStateChange = func(c *call.CallInfo) {
		if c.IsEnded() {
			s.removeCall(c.CallID)
			s.mgr.broker.endCall(c.CallID, string(c.StateData.EndReason))
			return
		}
		dir := "outbound"
		if c.Direction == core.CallDirectionIncoming {
			dir = "inbound"
		}
		existing, _ := s.mgr.broker.getCall(c.CallID)
		rec := CallRecord{
			SessionID: s.id, CallID: c.CallID, Direction: dir, Peer: c.PeerJid,
			StartedAt: time.Now().UnixMilli(), Status: mapStatus(c.StateData.State),
			Recording: s.recordingBase(c.CallID),
		}
		if existing != nil {
			rec.Owner = existing.Owner
			rec.StartedAt = existing.StartedAt
		}
		s.mgr.broker.upsertCall(rec)
	}
	cm.OnEnded = func(c *call.CallInfo) {
		s.removeCall(c.CallID)
		s.mgr.broker.endCall(c.CallID, string(c.StateData.EndReason))
	}
	cm.OnPeerAudio = func(pcm16 []float32) {
		ac, ok := s.reg.get(callID)
		if !ok {
			return
		}
		// Record first: the recording must not depend on a browser being attached.
		ac.rec.WritePeer(pcm16)
		if ac.bridge != nil {
			_ = ac.bridge.WritePCM(pcm16)
		}
	}
}

func (s *Session) startOutgoing(ctx context.Context, peer types.JID, isVideo, record bool) (string, error) {
	callID := signaling.GenerateCallID()
	cm := s.createCall(callID, s.shouldRecord(record))
	if err := cm.StartCall(ctx, callID, peer, isVideo); err != nil {
		s.removeCall(callID)
		return "", err
	}
	return callID, nil
}

func (s *Session) callForEvent(from types.JID, data *waBinary.Node) (*activeCall, bool) {
	callID := callIDFromNode(wrapCall(from, data))
	if callID == "" {
		return nil, false
	}
	return s.reg.get(callID)
}

func (s *Session) onIncomingOffer(ctx context.Context, evt *events.CallOffer) {
	node := wrapCall(evt.From, evt.Data)
	callID := callIDFromNode(node)
	if callID == "" {
		return
	}
	if max := s.mgr.maxCalls; max > 0 && s.reg.count() >= max {
		s.rejectOffer(ctx, node, evt.From)
		return
	}
	// Inbound calls carry no per-call flag, so they follow -record-all.
	cm := s.createCall(callID, s.shouldRecord(false))
	cm.HandleCallOffer(ctx, node, evt.From)
}

func (s *Session) rejectOffer(ctx context.Context, node *waBinary.Node, from types.JID) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return
	}
	creator := wanode.AttrString(info.InnerNode.Attrs, "call-creator")
	if creator == "" {
		creator = from.String()
	}
	reject := signaling.BuildRejectStanza(from, info.CallID, wanode.MustJID(creator))
	_ = wa.NewSocket(s.client).SendNode(ctx, reject)
	s.log.Info("inbound call rejected: session at capacity", "call_id", info.CallID)
}

func (s *Session) handleEvent(rawEvt any) {
	ctx := context.Background()
	switch evt := rawEvt.(type) {
	case *events.Connected:
		if id := s.client.Store.ID; id != nil {
			_ = s.mgr.store.setJID(s.mgr.appCtx, s.id, id.String())
		}
		s.setAuth(AuthSnapshot{State: "open", Paired: true})
	case *events.LoggedOut:
		s.setAuth(AuthSnapshot{State: "logged_out", Paired: false})
	case *events.ClientOutdated:
		s.log.Error("WhatsApp rejected the connection as outdated (405); update go.mau.fi/whatsmeow and redeploy")
	case *events.ConnectFailure:
		s.log.Error("WhatsApp refused the connection", "reason", evt.Reason, "message", evt.Message)
	case *events.CallOffer:
		s.onIncomingOffer(ctx, evt)
	case *events.CallAccept:
		if ac, ok := s.callForEvent(evt.From, evt.Data); ok {
			ac.cm.HandleCallAccept(ctx, wrapCall(evt.From, evt.Data), evt.From)
		}
	case *events.CallTransport:
		if ac, ok := s.callForEvent(evt.From, evt.Data); ok {
			ac.cm.HandleCallTransport(ctx, wrapCall(evt.From, evt.Data), evt.From)
		}
	case *events.CallTerminate:
		if ac, ok := s.callForEvent(evt.From, evt.Data); ok {
			ac.cm.HandleCallTerminate(wrapCall(evt.From, evt.Data))
		}
	case *events.CallReject:
		if ac, ok := s.callForEvent(evt.From, evt.Data); ok {
			ac.cm.HandleCallTerminate(wrapCall(evt.From, evt.Data))
		}
	}
}

func (s *Session) connect(ctx context.Context) error {
	if s.client.Store.ID != nil {
		return s.client.Connect()
	}
	return s.startPairing(ctx)
}

func (s *Session) startPairing(ctx context.Context) error {
	qrChan, err := s.client.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := s.client.Connect(); err != nil {
		return err
	}
	go func() {
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				s.log.Info("scan the QR code to pair this session")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				s.setAuth(AuthSnapshot{State: "qr", QR: evt.Code})
				s.mgr.broker.emitSessionQR(s.id, evt.Code)
			case "success":
				if id := s.client.Store.ID; id != nil {
					_ = s.mgr.store.setJID(s.mgr.appCtx, s.id, id.String())
				}
				s.setAuth(AuthSnapshot{State: "open", Paired: true})
			case "timeout":
				s.log.Info("QR pairing timed out; reconnect to get a new QR")
				s.setAuth(AuthSnapshot{State: "logged_out", Paired: false})
			default:
				// err-client-outdated, err-unexpected-state, error, ...: pairing is over
				// and no QR will follow, so say why instead of leaving the panel
				// waiting forever.
				s.log.Error("QR pairing failed", "event", evt.Event, "err", evt.Error)
				s.setAuth(AuthSnapshot{State: "logged_out", Paired: false})
			}
		}
	}()
	return nil
}

func (s *Session) setAuth(a AuthSnapshot) {
	s.mu.Lock()
	s.auth = a
	s.mu.Unlock()
	s.mgr.broker.emitAuthState(s.id, a)
	s.mgr.broker.emitSessionList(s.mgr.infos())
}

func (s *Session) info() SessionInfo {
	s.mu.Lock()
	a := s.auth
	s.mu.Unlock()
	jid := ""
	if id := s.client.Store.ID; id != nil {
		jid = id.String()
	}
	return SessionInfo{ID: s.id, Name: s.name, JID: jid, State: a.State, Paired: a.Paired || jid != ""}
}

func (s *Session) setBridge(callID string, b *Bridge) {
	oldB, found := s.reg.setBridge(callID, b)
	if !found {
		b.Close()
		return
	}
	if oldB != nil {
		oldB.Close()
	}
}

// capCallDuration ends a call after d, honouring duration_ms on the start
// request. A call that ends on its own first stops the timer in removeCall.
func (s *Session) capCallDuration(callID string, d time.Duration) {
	timer := time.AfterFunc(d, func() {
		if _, live := s.reg.get(callID); !live {
			return
		}
		s.log.Info("ending call at its requested duration", "call_id", callID, "after", d)
		// Tear down explicitly, the way doEndCall does, instead of waiting on the
		// state-machine callback: a forced end has to land even if the call is in
		// a state that produces no transition.
		s.terminateCall(callID, core.EndCallReasonUserEnded)
		s.removeCall(callID)
		s.mgr.broker.endCall(callID, string(core.EndCallReasonUserEnded))
	})
	if !s.reg.setCap(callID, timer) {
		timer.Stop() // the call was already gone
	}
}

func (s *Session) removeCall(callID string) {
	ac, ok := s.reg.remove(callID)
	if !ok {
		return
	}
	if ac.cap != nil {
		ac.cap.Stop()
	}
	if ac.bridge != nil {
		ac.bridge.Close()
	}
	ac.rec.Close()
}

func (s *Session) terminateCall(callID string, reason core.EndCallReason) {
	ac, ok := s.reg.get(callID)
	if !ok {
		return
	}
	_ = ac.cm.EndCall(context.Background(), reason)
}

func (s *Session) teardownAllCalls() {
	for _, ac := range s.reg.drain() {
		if ac.cap != nil {
			ac.cap.Stop()
		}
		_ = ac.cm.EndCall(context.Background(), core.EndCallReasonUserEnded)
		if ac.bridge != nil {
			ac.bridge.Close()
		}
		ac.rec.Close()
	}
}

func (s *Session) replaceClient(client *whatsmeow.Client) {
	s.teardownAllCalls()
	s.client.Disconnect()
	s.client = client
	client.AddEventHandler(s.handleEvent)
}

func (s *Session) shutdown() {
	s.teardownAllCalls()
	s.client.Disconnect()
}

func mapStatus(state core.CallState) CallStatus {
	switch state {
	case core.CallStateActive:
		return StatusConnected
	case core.CallStateEnded:
		return StatusEnded
	case core.CallStateInitiating:
		return StatusStarting
	default:
		return StatusRinging
	}
}
