package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wacalls/internal/voip/core"

	"go.mau.fi/whatsmeow/types"
)

func (s *server) routes() http.Handler {
	api := http.NewServeMux()

	api.HandleFunc("GET /api/sessions", s.handleSessionList)
	api.HandleFunc("POST /api/sessions", s.handleSessionCreate)
	api.HandleFunc("DELETE /api/sessions/{sid}", s.handleSessionDelete)
	api.HandleFunc("POST /api/sessions/{sid}/logout", s.handleSessionLogout)
	api.HandleFunc("POST /api/sessions/{sid}/pair", s.handleSessionPair)
	api.HandleFunc("POST /api/sessions/{sid}/calls", s.handleStartCall)
	api.HandleFunc("POST /api/sessions/{sid}/calls/{id}/webrtc", s.handleWebRTC)
	api.HandleFunc("POST /api/sessions/{sid}/calls/{id}/accept", s.handleAccept)
	api.HandleFunc("POST /api/sessions/{sid}/calls/{id}/reject", s.handleReject)
	api.HandleFunc("DELETE /api/sessions/{sid}/calls/{id}", s.handleEndCall)
	api.HandleFunc("GET /api/sessions/{sid}/history", s.handleHistory)
	api.HandleFunc("GET /api/sessions/{sid}/calls/{id}/recording", s.handleRecording)
	api.HandleFunc("GET /api/history", s.handleAllHistory)
	api.HandleFunc("GET /api/auth/check", s.handleAuthCheck)
	api.HandleFunc("GET /api/version", s.handleVersion)

	api.HandleFunc("GET /api/config", s.handleConfig)
	api.HandleFunc("GET /api/events", s.handleEvents)

	// Only /api is guarded; the static client is public so the browser can load
	// it and then present the token.
	root := http.NewServeMux()
	root.Handle("/api/", withCORS(s.origins, withAuth(s.token, s.apiKeys, api)))

	// Outside the guard on purpose: a container healthcheck has no credential, and
	// one that always got 401 would mark the service dead forever.
	root.HandleFunc("GET /healthz", s.handleHealth)

	if s.staticDir != "" {
		if _, err := os.Stat(s.staticDir); err == nil {
			root.Handle("/", http.FileServer(http.Dir(s.staticDir)))
		}
	}
	return root
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func clientID(r *http.Request) string {
	if id := r.Header.Get("X-Client-Id"); id != "" {
		return id
	}
	return r.URL.Query().Get("clientId")
}

func (s *server) sessionByID(w http.ResponseWriter, sid string) *Session {
	sess, ok := s.sessions.Get(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
		return nil
	}
	return sess
}

// Recording modes reported to the client so its UI can tell the operator what is
// actually happening, instead of offering a toggle that the server overrules.
const (
	recordingOff      = "off"      // no -record-dir: recording is unavailable
	recordingOptional = "optional" // recorded only when the request asks for it
	recordingAlways   = "always"   // -record-all: every call is recorded
)

func (s *server) recordingMode() string {
	switch {
	case s.recordDir == "":
		return recordingOff
	case s.recordAll:
		return recordingAlways
	default:
		return recordingOptional
	}
}

// handleHealth answers liveness probes. It is unauthenticated, so it carries
// nothing but the fact that the process is up — no version, no configuration, no
// session count. Anything else here would be a public information leak.
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleConfig reports the few server settings the UI has to agree with. Keep it
// to exactly that: it is not a place to dump configuration, and it must never
// carry paths, flags or secrets.
func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"recording": s.recordingMode()})
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	s.broker.serveSSE(w, r, clientID(r))
}

func (s *server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.sessions.infos()})
}

func (s *server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Session"
	}
	id, err := s.sessions.Create(name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Delete(r.Context(), r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleSessionLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Logout(r.Context(), r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleSessionPair(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Pair(r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleStartCall(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doStartCall(sess, w, r)
	}
}

func (s *server) handleWebRTC(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doWebRTC(sess, w, r)
	}
}

func (s *server) handleAccept(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doAccept(sess, w, r)
	}
}

func (s *server) handleReject(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doReject(sess, w, r)
	}
}

func (s *server) handleEndCall(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doEndCall(sess, w, r)
	}
}

func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		writeJSON(w, http.StatusOK, map[string]any{"rows": s.broker.historyRows(r.Context(), sess.id, historyLimit(r, 50))})
	}
}

// handleAllHistory serves ended calls across every session, newest first, for
// the panel's history page. ?session= narrows it to one session.
func (s *server) handleAllHistory(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session")
	writeJSON(w, http.StatusOK, map[string]any{"rows": s.broker.historyRows(r.Context(), sid, historyLimit(r, 100))})
}

// handleAuthCheck lets the panel's login screen validate a token: it sits behind
// withAuth, so reaching it at all means the credential was accepted.
func (s *server) handleAuthCheck(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// historyLimit reads ?limit=, falling back to def and capping at 500.
func historyLimit(r *http.Request, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, 500)
}

// handleRecording serves a finished call's recording: one stereo file with the
// operator on the left channel and the remote party on the right. The route lives
// behind the same origin and token guards as the rest of /api.
func (s *server) handleRecording(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	if s.recordDir == "" || s.store == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recording is not enabled"})
		return
	}

	base, err := s.store.recordingBase(r.Context(), sess.id, r.PathValue("id"))
	if err != nil || base == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no recording for this call"})
		return
	}

	path, err := s.recordingPath(sess.id, base)
	if err != nil {
		s.log.Warn("rejected recording path", "session", sess.id, "base", base, "err", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no recording for this call"})
		return
	}
	if _, err := os.Stat(path); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recording file is gone"})
		return
	}

	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition", `attachment; filename="`+base+`.wav"`)
	http.ServeFile(w, r, path)
}

// recordingPath builds the on-disk path and proves it stays inside the recording
// directory. The base comes back out of the database, so it is re-validated here
// rather than trusted from the write path.
func (s *server) recordingPath(sessionID, base string) (string, error) {
	root, err := filepath.Abs(s.recordDir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, safeName(sessionID), safeName(base)+".wav")
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the recording directory", rel)
	}
	return full, nil
}

func (s *server) doStartCall(sess *Session, w http.ResponseWriter, r *http.Request) {
	if sess.client.Store.ID == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not paired"})
		return
	}
	var body struct {
		Phone      string `json:"phone"`
		DurationMs int    `json:"duration_ms"`
		Record     bool   `json:"record"`
		Operator   string `json:"operator"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Phone) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone required"})
		return
	}
	// Refuse rather than silently drop the request: a caller that asked to record
	// and got a call back would reasonably assume there is a recording.
	if body.Record && s.recordDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recording requested but the server was started without -record-dir"})
		return
	}

	owner, busy := s.callOwner(r, body.Operator)
	if busy {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "operator already on a call"})
		return
	}

	if max := s.sessions.maxCalls; max > 0 && sess.reg.count() >= max {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "max concurrent calls"})
		return
	}
	peer := types.NewJID(normalizePhone(body.Phone), types.DefaultUserServer)

	callID, err := sess.startOutgoing(r.Context(), peer, false, body.Record)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if body.DurationMs > 0 {
		sess.capCallDuration(callID, time.Duration(body.DurationMs)*time.Millisecond)
	}
	s.broker.upsertCall(CallRecord{
		SessionID: sess.id, CallID: callID, Owner: ownerPointer(owner), Direction: "outbound", Peer: peer.String(),
		StartedAt: time.Now().UnixMilli(), Status: StatusRinging,
		Recording: sess.recordingBase(callID),
	})
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": callID}})
}

// callOwner decides who owns a call that is about to start, and reports whether
// the request has to be refused because that operator is already talking.
//
// An integration is not an operator: it never takes the audio, so the call it
// starts is left unclaimed for a browser to pick up — or reserved for the
// operator it names — and the one-call-at-a-time rule does not apply to it. That
// rule is about a human having one mouth.
func (s *server) callOwner(r *http.Request, requestedOperator string) (owner string, busy bool) {
	if callerFrom(r).isIntegration() {
		return strings.TrimSpace(requestedOperator), false
	}
	operator := clientID(r)
	if other := s.broker.ownerActiveCall(operator); other != "" {
		return "", true
	}
	return operator, false
}

// ownerPointer keeps "no owner" as a nil pointer. A pointer to the empty string
// would read as a real owner nobody can match, which is what used to make a call
// started over the API impossible for any browser to claim.
func ownerPointer(owner string) *string {
	if owner == "" {
		return nil
	}
	return &owner
}

func (s *server) doWebRTC(sess *Session, w http.ResponseWriter, r *http.Request) {
	callID := r.PathValue("id")
	ac, ok := sess.reg.get(callID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	// Attaching audio is how a call is claimed: an unowned one — typically started
	// by an integration — becomes this operator's, and after that nobody else can
	// take it over.
	operator := clientID(r)
	if !s.broker.setOwner(callID, operator) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call owned by another client"})
		return
	}
	var body struct {
		SDPOffer string `json:"sdp_offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SDPOffer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sdp_offer required"})
		return
	}
	cb := BridgeCallbacks{
		OnBrowserPCM: func(pcm []float32) {
			ac.rec.WriteLocal(pcm)
			ac.cm.FeedCapturedPCM(pcm)
		},
		OnTerminalICE: func() { go sess.terminateCall(callID, core.EndCallReasonUserEnded) },
	}
	bridge, answer, err := NewBridge(s.browserAPI, body.SDPOffer, cb, s.log)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	sess.setBridge(callID, bridge)
	// Tell the other browsers this one is taken; setOwner only changes memory.
	s.broker.rebroadcastCall(callID)
	writeJSON(w, http.StatusOK, map[string]string{"sdp_answer": answer})
}

func (s *server) doAccept(sess *Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ac, ok := sess.reg.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	owner := clientID(r)
	if other := s.broker.ownerActiveCall(owner); other != "" && other != id {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "operator already on a call"})
		return
	}
	if !s.broker.setOwner(id, owner) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "claimed by another client"})
		return
	}
	s.broker.emitIncomingClaimed(sess.id, id, owner)
	if err := ac.cm.AcceptCall(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": id}})
}

func (s *server) doReject(sess *Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.broker.mayControl(id, clientID(r)) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call owned by another client"})
		return
	}
	if ac, ok := sess.reg.get(id); ok {
		_ = ac.cm.RejectCall(r.Context(), id, core.EndCallReasonDeclined)
	}
	sess.removeCall(id)
	s.broker.endCall(id, string(core.EndCallReasonDeclined))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) doEndCall(sess *Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.broker.mayControl(id, clientID(r)) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call owned by another client"})
		return
	}
	if ac, ok := sess.reg.get(id); ok {
		_ = ac.cm.EndCall(r.Context(), core.EndCallReasonUserEnded)
	}
	sess.removeCall(id)
	s.broker.endCall(id, string(core.EndCallReasonUserEnded))
	w.WriteHeader(http.StatusNoContent)
}

func normalizePhone(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "+")
	var b strings.Builder
	for _, c := range p {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
