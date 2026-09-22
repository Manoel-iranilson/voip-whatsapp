package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wacalls/internal/voip/call"

	"go.mau.fi/whatsmeow/types"
)

// newTestServer builds a server around an in-memory session manager. No
// WhatsApp connection is ever made: the tests only exercise routes that stop
// before touching the client.
func newTestServer(t *testing.T, origins, token string) *server {
	t.Helper()
	return newTestServerWithConfig(t, config{allowOrigin: origins, token: token})
}

func newTestServerWithConfig(t *testing.T, cfg config) *server {
	t.Helper()
	mgr := newTestManagerWithConfig(t, cfg)
	return &server{
		broker:    mgr.broker,
		sessions:  mgr,
		log:       slog.Default(),
		origins:   newOriginPolicy(cfg.allowOrigin),
		token:     cfg.token,
		store:     mgr.store,
		recordDir: cfg.recordDir,
		recordAll: cfg.recordAll,
		apiKeys:   cfg.apiKeys,
	}
}

func do(t *testing.T, h http.Handler, method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------- origins

func TestOriginPolicyDecisions(t *testing.T) {
	cases := []struct {
		name   string
		spec   string
		origin string
		host   string
		want   bool
	}{
		{"no origin header is not a browser", "", "", "localhost:8080", true},
		{"same origin allowed by default", "", "http://localhost:8080", "localhost:8080", true},
		{"foreign origin denied by default", "", "http://evil.example", "localhost:8080", false},
		{"foreign origin allowed when listed", "http://ops.local", "http://ops.local", "localhost:8080", true},
		{"trailing slash and case ignored", "HTTP://Ops.Local/", "http://ops.local", "localhost:8080", true},
		{"unlisted origin still denied", "http://ops.local", "http://evil.example", "localhost:8080", false},
		{"wildcard allows anything", "*", "http://evil.example", "localhost:8080", true},
		{"opaque origin denied", "", "null", "localhost:8080", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "http://"+tc.host+"/api/sessions", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := newOriginPolicy(tc.spec).permits(req); got != tc.want {
				t.Fatalf("permits(origin=%q, spec=%q) = %v, want %v", tc.origin, tc.spec, got, tc.want)
			}
		})
	}
}

// A cross-origin POST with a simple content type needs no preflight, so the
// browser runs it and only hides the response. The side effect has to be blocked
// on the server, before the handler.
func TestForeignOriginBlockedBeforeHandler(t *testing.T) {
	srv := newTestServer(t, "", "")
	h := srv.routes()

	foreign := map[string]string{"Origin": "http://evil.example", "Content-Type": "text/plain"}
	rec := do(t, h, "POST", "http://example.com/api/sessions/nope/calls/c1/reject", foreign, "{}")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin POST: got %d, want 403", rec.Code)
	}

	rec = do(t, h, "GET", "http://example.com/api/sessions", foreign, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin GET: got %d, want 403", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("a denied origin must not get an Allow-Origin header")
	}
}

func TestSameOriginReachesHandler(t *testing.T) {
	srv := newTestServer(t, "", "")
	h := srv.routes()

	same := map[string]string{"Origin": "http://example.com", "Content-Type": "application/json"}
	// Unknown session: a 404 proves the request got past the origin check.
	rec := do(t, h, "POST", "http://example.com/api/sessions/nope/calls/c1/reject", same, "{}")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("same-origin POST: got %d, want 404 from the handler", rec.Code)
	}

	rec = do(t, h, "GET", "http://example.com/api/sessions", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("origin-less GET: got %d, want 200", rec.Code)
	}
}

func TestListedOriginGetsCORSHeaders(t *testing.T) {
	srv := newTestServer(t, "http://ops.local:5173", "")
	h := srv.routes()

	headers := map[string]string{"Origin": "http://ops.local:5173"}
	rec := do(t, h, "OPTIONS", "http://example.com/api/sessions", headers, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: got %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://ops.local:5173" {
		t.Fatalf("Allow-Origin = %q, want the echoed origin", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
		t.Fatal("an allowlisted response must Vary on Origin")
	}
}

// ------------------------------------------------------------------ token

func TestTokenGuard(t *testing.T) {
	srv := newTestServer(t, "", "s3cret")
	h := srv.routes()

	if rec := do(t, h, "GET", "http://example.com/api/sessions", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}
	wrong := map[string]string{"X-Auth-Token": "nope"}
	if rec := do(t, h, "GET", "http://example.com/api/sessions", wrong, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", rec.Code)
	}
	right := map[string]string{"X-Auth-Token": "s3cret"}
	if rec := do(t, h, "GET", "http://example.com/api/sessions", right, ""); rec.Code != http.StatusOK {
		t.Fatalf("header token: got %d, want 200", rec.Code)
	}
	bearer := map[string]string{"Authorization": "Bearer s3cret"}
	if rec := do(t, h, "GET", "http://example.com/api/sessions", bearer, ""); rec.Code != http.StatusOK {
		t.Fatalf("bearer token: got %d, want 200", rec.Code)
	}
	// EventSource cannot set headers, so the query parameter has to work too.
	if rec := do(t, h, "GET", "http://example.com/api/sessions?token=s3cret", nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("query token: got %d, want 200", rec.Code)
	}
}

// -------------------------------------------------------------- ownership

// registerCall puts a call in the session registry and gives the broker an owner
// for it, the state reached after a start or an accept.
func registerCall(t *testing.T, srv *server, callID, owner string) *Session {
	t.Helper()
	sess := srv.sessions.addUnconnected(t, "Account")
	sess.reg.add(callID, &activeCall{cm: call.NewCallManager(nil, slog.Default())})
	srv.broker.upsertCall(CallRecord{
		SessionID: sess.id, CallID: callID, Owner: &owner,
		Direction: "outbound", Peer: "5511999@s.whatsapp.net", Status: StatusConnected,
	})
	return sess
}

func TestWebRTCRefusesNonOwner(t *testing.T) {
	srv := newTestServer(t, "", "")
	sess := registerCall(t, srv, "call-1", "op-A")
	h := srv.routes()

	path := "http://example.com/api/sessions/" + sess.id + "/calls/call-1/webrtc"
	intruder := map[string]string{"X-Client-Id": "op-B", "Content-Type": "application/json"}
	rec := do(t, h, "POST", path, intruder, `{"sdp_offer":"v=0"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("intruder attaching a bridge: got %d, want 409", rec.Code)
	}
	if _, ok := sess.reg.get("call-1"); !ok {
		t.Fatal("the call must survive a refused takeover")
	}
	if ac, _ := sess.reg.get("call-1"); ac.bridge != nil {
		t.Fatal("no bridge should have been attached for the intruder")
	}
}

func TestEndAndRejectRefuseNonOwner(t *testing.T) {
	srv := newTestServer(t, "", "")
	sess := registerCall(t, srv, "call-2", "op-A")
	h := srv.routes()

	base := "http://example.com/api/sessions/" + sess.id + "/calls/call-2"
	intruder := map[string]string{"X-Client-Id": "op-B", "Content-Type": "application/json"}

	if rec := do(t, h, "DELETE", base, intruder, ""); rec.Code != http.StatusConflict {
		t.Fatalf("intruder ending a call: got %d, want 409", rec.Code)
	}
	if rec := do(t, h, "POST", base+"/reject", intruder, "{}"); rec.Code != http.StatusConflict {
		t.Fatalf("intruder rejecting a call: got %d, want 409", rec.Code)
	}
	if _, ok := sess.reg.get("call-2"); !ok {
		t.Fatal("refused requests must leave the call running")
	}

	owner := map[string]string{"X-Client-Id": "op-A", "Content-Type": "application/json"}
	if rec := do(t, h, "DELETE", base, owner, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("owner ending its own call: got %d, want 204", rec.Code)
	}
	if _, ok := sess.reg.get("call-2"); ok {
		t.Fatal("the owner's end request should have removed the call")
	}
}

// ---------------------------------------------------------------- config

func TestConfigEndpointReportsRecordingMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  config
		want string
	}{
		{"no directory means unavailable", config{}, recordingOff},
		{"directory alone is opt-in", config{recordDir: "d"}, recordingOptional},
		{"record-all forces every call", config{recordDir: "d", recordAll: true}, recordingAlways},
		{"record-all without a directory is still off", config{recordAll: true}, recordingOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServerWithConfig(t, tc.cfg)
			res := do(t, srv.routes(), "GET", "http://example.com/api/config", nil, "")
			if res.Code != http.StatusOK {
				t.Fatalf("got %d, want 200", res.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["recording"] != tc.want {
				t.Fatalf("recording = %q, want %q", body["recording"], tc.want)
			}
		})
	}
}

func TestConfigEndpointIsGuarded(t *testing.T) {
	srv := newTestServerWithConfig(t, config{token: "s3cret"})
	h := srv.routes()

	if res := do(t, h, "GET", "http://example.com/api/config", nil, ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", res.Code)
	}
	withToken := map[string]string{"X-Auth-Token": "s3cret"}
	if res := do(t, h, "GET", "http://example.com/api/config", withToken, ""); res.Code != http.StatusOK {
		t.Fatalf("with token: got %d, want 200", res.Code)
	}
	foreign := map[string]string{"X-Auth-Token": "s3cret", "Origin": "http://evil.example"}
	if res := do(t, h, "GET", "http://example.com/api/config", foreign, ""); res.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: got %d, want 403", res.Code)
	}
}

// The endpoint exists so the UI can agree with the server; it must not grow into
// a configuration dump that leaks paths or secrets.
func TestConfigEndpointExposesOnlyTheMode(t *testing.T) {
	srv := newTestServerWithConfig(t, config{recordDir: `C:\secret\path`, token: "s3cret", recordAll: true})
	res := do(t, srv.routes(), "GET", "http://example.com/api/config",
		map[string]string{"X-Auth-Token": "s3cret"}, "")

	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 {
		t.Fatalf("config should carry exactly one field, got %v", body)
	}
	if raw := res.Body.String(); strings.Contains(raw, "secret") {
		t.Fatalf("config leaked a path or token: %s", raw)
	}
}

// ------------------------------------------------------------- recording

// seedEndedCall writes a finished call to history, the state the download route
// reads from, and returns its recording base.
func seedEndedCall(t *testing.T, srv *server, sess *Session, callID, base string) {
	t.Helper()
	ended := time.Now().UnixMilli()
	err := srv.store.appendCall(context.Background(), CallRecord{
		SessionID: sess.id, CallID: callID, Direction: "outbound",
		Peer: "5511999@s.whatsapp.net", StartedAt: ended - 1000,
		EndedAt: &ended, EndReason: "user_ended", Recording: base,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecordingDownload(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithConfig(t, config{recordDir: dir})
	sess := srv.sessions.addUnconnected(t, "Account")
	h := srv.routes()

	// Produce a real recording the way a call would.
	rec, err := newCallRecorder(dir, sess.id, "CALL9", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	rec.WriteLocal(make([]float32, 320))
	rec.WritePeer(make([]float32, 320))
	rec.flushFrame()
	rec.Close()
	seedEndedCall(t, srv, sess, "CALL9", rec.Base())

	res := do(t, h, "GET", "http://example.com/api/sessions/"+sess.id+"/calls/CALL9/recording", nil, "")
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", res.Code)
	}
	if ct := res.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("Content-Type = %q, want audio/wav", ct)
	}
	body := res.Body.Bytes()
	if string(body[0:4]) != "RIFF" {
		t.Fatal("body is not a WAV file")
	}
	// One stereo frame of 320 sample pairs: 320 * 2ch * 2 bytes.
	if len(body) != wavHeaderSize+320*4 {
		t.Fatalf("body is %d bytes, want %d", len(body), wavHeaderSize+320*4)
	}
	if got := binary.LittleEndian.Uint16(body[22:]); got != 2 {
		t.Fatalf("downloaded file has %d channels, want 2", got)
	}
}

func TestRecordingDownloadRejections(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithConfig(t, config{recordDir: dir})
	sess := srv.sessions.addUnconnected(t, "Account")
	h := srv.routes()
	prefix := "http://example.com/api/sessions/" + sess.id + "/calls/"

	// A call nobody recorded.
	seedEndedCall(t, srv, sess, "PLAIN", "")
	if res := do(t, h, "GET", prefix+"PLAIN/recording", nil, ""); res.Code != http.StatusNotFound {
		t.Fatalf("unrecorded call: got %d, want 404", res.Code)
	}
	// A call that never happened.
	if res := do(t, h, "GET", prefix+"GHOST/recording", nil, ""); res.Code != http.StatusNotFound {
		t.Fatalf("unknown call: got %d, want 404", res.Code)
	}
	// A history row whose recording base points outside the directory.
	seedEndedCall(t, srv, sess, "EVIL", "../../../../etc/passwd")
	if res := do(t, h, "GET", prefix+"EVIL/recording", nil, ""); res.Code != http.StatusNotFound {
		t.Fatalf("traversal base: got %d, want 404", res.Code)
	}
}

func TestRecordingPathStaysInsideRoot(t *testing.T) {
	srv := &server{recordDir: t.TempDir()}
	root, err := filepath.Abs(srv.recordDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"../../escape", `..\..\escape`, "/abs/path", "ok-123"} {
		got, err := srv.recordingPath("sess", base)
		if err != nil {
			continue // rejected outright, which is also fine
		}
		if !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Fatalf("base %q resolved to %q, outside %q", base, got, root)
		}
	}
}

func TestRecordingDisabledWithoutDir(t *testing.T) {
	srv := newTestServerWithConfig(t, config{})
	sess := srv.sessions.addUnconnected(t, "Account")
	h := srv.routes()

	res := do(t, h, "GET", "http://example.com/api/sessions/"+sess.id+"/calls/X/recording", nil, "")
	if res.Code != http.StatusNotFound {
		t.Fatalf("download with recording off: got %d, want 404", res.Code)
	}

	// Asking to record when the server cannot must fail loudly, not silently.
	// The session has to look paired, otherwise the handler stops at 503 first —
	// and the check has to land before anything touches the network.
	markPaired(t, sess)
	body := `{"phone":"5511999999999","record":true}`
	headers := map[string]string{"Content-Type": "application/json", "X-Client-Id": "op-A"}
	res = do(t, h, "POST", "http://example.com/api/sessions/"+sess.id+"/calls", headers, body)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("record:true with no -record-dir: got %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "record-dir") {
		t.Fatalf("error should name the missing flag, got %q", res.Body.String())
	}
}

// markPaired makes a test session pass the "not paired" gate without any
// network. Nothing else in these tests depends on the JID being real.
func markPaired(t *testing.T, s *Session) {
	t.Helper()
	jid := types.NewJID("5511888888888", types.DefaultUserServer)
	s.client.Store.ID = &jid
}

func TestShouldRecord(t *testing.T) {
	cases := []struct {
		name      string
		dir       string
		recordAll bool
		requested bool
		want      bool
	}{
		{"off without a directory", "", false, true, false},
		{"record-all without a directory", "", true, true, false},
		{"opt-in per call", "d", false, true, true},
		{"not requested, not forced", "d", false, false, false},
		{"record-all covers inbound", "d", true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServerWithConfig(t, config{recordDir: tc.dir, recordAll: tc.recordAll})
			sess := srv.sessions.addUnconnected(t, "Account")
			if got := sess.shouldRecord(tc.requested); got != tc.want {
				t.Fatalf("shouldRecord(%v) = %v, want %v", tc.requested, got, tc.want)
			}
		})
	}
}

func TestUnclaimedCallIsOpenToFirstClient(t *testing.T) {
	srv := newTestServer(t, "", "")
	sess := srv.sessions.addUnconnected(t, "Account")
	sess.reg.add("call-3", &activeCall{cm: call.NewCallManager(nil, slog.Default())})
	srv.broker.upsertCall(CallRecord{
		SessionID: sess.id, CallID: "call-3", Direction: "inbound",
		Peer: "5511999@s.whatsapp.net", Status: StatusRinging,
	})

	if !srv.broker.mayControl("call-3", "op-A") {
		t.Fatal("an unowned call should be claimable")
	}
	if !srv.broker.mayControl("unknown-call", "op-A") {
		t.Fatal("a call the broker never saw is the session registry's business")
	}
}
