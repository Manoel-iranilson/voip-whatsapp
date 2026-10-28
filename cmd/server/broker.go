package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type CallStatus string

const (
	StatusStarting  CallStatus = "starting"
	StatusRinging   CallStatus = "ringing"
	StatusConnected CallStatus = "connected"
	StatusEnded     CallStatus = "ended"
)

type CallRecord struct {
	SessionID string     `json:"sessionId"`
	CallID    string     `json:"callId"`
	Owner     *string    `json:"owner"`
	Direction string     `json:"direction"`
	Peer      string     `json:"peer"`
	StartedAt int64      `json:"startedAt"`
	Status    CallStatus `json:"status"`
	EndedAt   *int64     `json:"endedAt,omitempty"`
	EndReason string     `json:"endReason,omitempty"`
	// Recording is the file name stem of this call's recordings, empty when it
	// was not recorded. The individual files are <Recording>-local.wav and
	// <Recording>-peer.wav under the session's recording directory.
	Recording string `json:"recording,omitempty"`
}

type AuthSnapshot struct {
	State  string `json:"state"`
	Paired bool   `json:"paired"`
	QR     string `json:"qr,omitempty"`
}

type SessionInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	JID    string `json:"jid"`
	State  string `json:"state"`
	Paired bool   `json:"paired"`
}

// maxRecentHistory caps the in-process history cache. The durable copy lives in
// SQLite; this slice only keeps reads cheap and gives a fallback when no store
// is attached (tests).
const maxRecentHistory = 200

// historyStore persists ended calls. It is satisfied by *sessionStore; a nil
// store degrades the broker to memory-only history.
type historyStore interface {
	appendCall(ctx context.Context, r CallRecord) error
	recentCalls(ctx context.Context, sessionID string, limit int) ([]CallRecord, error)
}

type subscriber struct {
	clientID string
	ch       chan []byte
	// resync is poked when an event had to be dropped because ch was full. The
	// SSE loop answers it with a fresh full snapshot, so a slow client resynchronizes
	// instead of being left on a stale view with no way to notice.
	resync chan struct{}
}

type Broker struct {
	mu     sync.RWMutex
	subs   map[*subscriber]struct{}
	calls  map[string]*CallRecord
	recent []CallRecord

	history    historyStore
	webhooks   *webhookSender
	SnapshotFn func() []any
}

func (b *Broker) closeWebhooks() { b.webhooks.Close() }

func NewBroker() *Broker {
	return &Broker{
		subs:  map[*subscriber]struct{}{},
		calls: map[string]*CallRecord{},
	}
}

func (b *Broker) subscribe(clientID string) *subscriber {
	s := &subscriber{
		clientID: clientID,
		ch:       make(chan []byte, 32),
		resync:   make(chan struct{}, 1),
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *Broker) unsubscribe(s *subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	close(s.ch)
}

func (b *Broker) broadcast(ev any) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- data:
		default:
			// Buffer full: the client is behind. Ask it to resync rather than
			// silently losing a state change.
			select {
			case s.resync <- struct{}{}:
			default:
			}
		}
	}
}

func (b *Broker) emitAuthState(sessionID string, a AuthSnapshot) {
	b.broadcast(map[string]any{
		"type": "auth-state", "sessionId": sessionID,
		"paired": a.Paired, "state": a.State, "qr": a.QR,
	})
}

func (b *Broker) emitSessionList(sessions []SessionInfo) {
	b.broadcast(map[string]any{"type": "session-list", "sessions": sessions})
}

func (b *Broker) emitSessionQR(sessionID, qr string) {
	b.broadcast(map[string]any{"type": "session-qr", "sessionId": sessionID, "qr": qr})
}

func (b *Broker) upsertCall(r CallRecord) {
	b.mu.Lock()
	previous, existed := b.calls[r.CallID]
	statusChanged := !existed || previous.Status != r.Status
	cp := r
	b.calls[r.CallID] = &cp
	b.mu.Unlock()

	b.broadcastCallList()
	b.broadcast(map[string]any{
		"type": "call-status", "sessionId": r.SessionID, "id": r.CallID, "owner": r.Owner,
		"status": r.Status, "peer": r.Peer, "startedAt": r.StartedAt,
	})

	// upsertCall fires on every transition, several of which land on the same
	// status. Webhook consumers want the change, not the churn.
	if statusChanged {
		if ev, ok := webhookEventFor(r); ok {
			b.webhooks.send(ev)
		}
	}
}

func (b *Broker) getCall(id string) (*CallRecord, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	c, ok := b.calls[id]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

func (b *Broker) setOwner(id, owner string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.calls[id]
	if !ok {
		return false
	}
	if c.Owner != nil && *c.Owner != owner {
		return false
	}
	c.Owner = &owner
	return true
}

// mayControl reports whether client is allowed to drive callID — attach a media
// bridge to it, end it or reject it. A call nobody has claimed yet is open to the
// first client; once claimed it answers only to its owner, so a second browser
// cannot take over the audio of a call in progress.
func (b *Broker) mayControl(callID, client string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	c, ok := b.calls[callID]
	if !ok {
		return true // unknown to the broker: the session registry is the authority
	}
	return c.Owner == nil || *c.Owner == client
}

// rebroadcastCall re-publishes a call whose record changed in memory without
// going through upsertCall — setOwner is the one that does that.
func (b *Broker) rebroadcastCall(id string) {
	c, ok := b.getCall(id)
	if !ok {
		return
	}
	b.broadcastCallList()
	b.broadcast(map[string]any{
		"type": "call-status", "sessionId": c.SessionID, "id": c.CallID, "owner": c.Owner,
		"status": c.Status, "peer": c.Peer, "startedAt": c.StartedAt,
	})
}

func (b *Broker) ownerActiveCall(owner string) string {
	if owner == "" {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, c := range b.calls {
		if c.Owner != nil && *c.Owner == owner && c.Status != StatusEnded {
			return id
		}
	}
	return ""
}

func (b *Broker) endCall(id, reason string) {
	b.mu.Lock()
	c, ok := b.calls[id]
	if !ok {
		b.mu.Unlock()
		return
	}
	now := time.Now().UnixMilli()
	c.Status = StatusEnded
	c.EndedAt = &now
	c.EndReason = reason
	ended := *c
	delete(b.calls, id)
	b.recent = append(b.recent, ended)
	if n := len(b.recent); n > maxRecentHistory {
		b.recent = append(b.recent[:0], b.recent[n-maxRecentHistory:]...)
	}
	owner := c.Owner
	sessionID := c.SessionID
	b.mu.Unlock()

	if b.history != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.history.appendCall(ctx, ended); err != nil {
			slog.Warn("could not persist call history", "call_id", id, "err", err)
		}
	}

	b.broadcast(map[string]any{
		"type": "call-ended", "sessionId": sessionID, "id": id, "owner": owner, "reason": reason, "endedAt": now,
	})
	b.broadcastCallList()

	if ev, ok := webhookEventFor(ended); ok {
		b.webhooks.send(ev)
	}
}

func (b *Broker) broadcastCallList() {
	b.mu.RLock()
	list := make([]CallRecord, 0, len(b.calls))
	for _, c := range b.calls {
		list = append(list, *c)
	}
	b.mu.RUnlock()
	b.broadcast(map[string]any{"type": "call-list", "calls": list})
}

func (b *Broker) emitIncoming(sessionID, id, peer string) {
	now := time.Now().UnixMilli()
	b.broadcast(map[string]any{
		"type": "incoming", "sessionId": sessionID, "id": id, "peer": peer, "offeredAt": now,
	})
	b.webhooks.send(webhookEvent{
		Type: eventCallIncoming, SessionID: sessionID, CallID: id,
		Direction: "inbound", Peer: peer, Status: string(StatusRinging), StartedAt: now,
	})
}

func (b *Broker) emitIncomingClaimed(sessionID, id, owner string) {
	b.broadcast(map[string]any{"type": "incoming-claimed", "sessionId": sessionID, "id": id, "owner": owner})
}

// historyRows returns the most recent ended calls, newest first. It prefers the
// durable store so history survives a restart, and falls back to the in-process
// cache when there is no store or the query fails.
func (b *Broker) historyRows(ctx context.Context, sessionID string, limit int) []CallRecord {
	if b.history != nil {
		rows, err := b.history.recentCalls(ctx, sessionID, limit)
		if err == nil {
			return rows
		}
		slog.Warn("could not read call history; serving in-memory copy", "err", err)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	rows := make([]CallRecord, 0, limit)
	for i := len(b.recent) - 1; i >= 0 && len(rows) < limit; i-- {
		if sessionID == "" || b.recent[i].SessionID == sessionID {
			rows = append(rows, b.recent[i])
		}
	}
	return rows
}

func (b *Broker) serveSSE(w http.ResponseWriter, r *http.Request, clientID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tells a buffering reverse proxy (nginx and friends) to pass events through
	// as they are written; otherwise a QR can sit in its buffer until it expires.
	w.Header().Set("X-Accel-Buffering", "no")
	// CORS is decided by the origin policy in front of this handler; setting a
	// wildcard here would hand every event stream to any origin.

	sub := b.subscribe(clientID)
	defer b.unsubscribe(sub)

	b.writeSnapshot(w, flusher)

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-sub.ch:
			if _, err := w.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
		case <-sub.resync:
			// This client missed at least one event. Replace its whole view.
			b.writeSnapshot(w, flusher)
		case <-keepalive.C:
			w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

// writeSnapshot pushes the complete current state down one connection: the
// session list plus every live call.
func (b *Broker) writeSnapshot(w http.ResponseWriter, f http.Flusher) {
	if b.SnapshotFn != nil {
		for _, ev := range b.SnapshotFn() {
			writeSSE(w, f, ev)
		}
	}
	b.mu.RLock()
	list := make([]CallRecord, 0, len(b.calls))
	for _, c := range b.calls {
		list = append(list, *c)
	}
	b.mu.RUnlock()
	writeSSE(w, f, map[string]any{"type": "call-list", "calls": list})
}

func writeSSE(w http.ResponseWriter, f http.Flusher, ev any) {
	data, _ := json.Marshal(ev)
	w.Write(append(append([]byte("data: "), data...), '\n', '\n'))
	f.Flush()
}
