package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wacalls/internal/voip/call"
)

// These cover the two things that made WaCalls unusable from another system: a
// call started over the API could never be claimed by a browser, and the second
// such call was refused outright.
//
// A real POST /calls cannot complete here — placing a call needs a live WhatsApp
// connection — so the ownership decision is exercised through callOwner, and the
// HTTP tests assert the parts that resolve before the network is touched.

const testAPIKey = "crm-secret"

func newIntegrationServer(t *testing.T) (*server, *Session, http.Handler) {
	t.Helper()
	srv := newTestServerWithConfig(t, config{
		token:   "operator-token",
		apiKeys: apiKeys{"crm": testAPIKey},
	})
	sess := srv.sessions.addUnconnected(t, "Account")
	markPaired(t, sess)
	return srv, sess, srv.routes()
}

func asIntegration() map[string]string {
	return map[string]string{"X-API-Key": testAPIKey, "Content-Type": "application/json"}
}

func asOperator(id string) map[string]string {
	return map[string]string{
		"X-Auth-Token": "operator-token",
		"X-Client-Id":  id,
		"Content-Type": "application/json",
	}
}

// newInertCallManager is a CallManager with no socket and no call in progress:
// EndCall returns immediately, which is all these tests need from it.
func newInertCallManager() *call.CallManager { return call.NewCallManager(nil, slog.Default()) }

// authed runs a request through the auth middleware so callOwner sees the same
// caller a real handler would.
func authed(t *testing.T, srv *server, headers map[string]string) *http.Request {
	t.Helper()
	var captured *http.Request
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { captured = r })
	req := httptest.NewRequest("POST", "http://example.com/api/sessions/x/calls", strings.NewReader("{}"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	withAuth(srv.token, srv.apiKeys, inner).ServeHTTP(httptest.NewRecorder(), req)
	if captured == nil {
		t.Fatal("auth rejected a request the test expected to pass")
	}
	return captured
}

// A call started by an integration belongs to nobody, so a browser can take it.
// Owner used to be a pointer to the empty string, which matched no client and
// locked everyone out.
func TestIntegrationCallStartsUnowned(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	owner, busy := srv.callOwner(authed(t, srv, asIntegration()), "")
	if busy {
		t.Fatal("an integration is never 'already on a call'")
	}
	if owner != "" {
		t.Fatalf("owner = %q, want empty so a browser can claim it", owner)
	}
	if ownerPointer(owner) != nil {
		t.Fatal("no owner must be a nil pointer, not a pointer to the empty string")
	}
}

// An integration is not an operator, so the one-live-call rule must not apply to
// it. This is the regression that limited an entire deployment to one call.
func TestIntegrationMayStartSeveralCalls(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	// One call from this integration is already running and unclaimed.
	srv.broker.upsertCall(CallRecord{
		SessionID: "s1", CallID: "c1", Owner: nil,
		Direction: "outbound", Peer: "5511999@s.whatsapp.net", Status: StatusConnected,
	})
	// And one it reserved for an operator.
	reserved := "op-A"
	srv.broker.upsertCall(CallRecord{
		SessionID: "s1", CallID: "c2", Owner: &reserved,
		Direction: "outbound", Peer: "5511888@s.whatsapp.net", Status: StatusConnected,
	})

	if _, busy := srv.callOwner(authed(t, srv, asIntegration()), ""); busy {
		t.Fatal("an integration must be able to start another call")
	}
}

// An operator is still held to one call at a time.
func TestOperatorStillLimitedToOneCall(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	owner := "op-A"
	srv.broker.upsertCall(CallRecord{
		SessionID: "s1", CallID: "c1", Owner: &owner,
		Direction: "outbound", Peer: "5511999@s.whatsapp.net", Status: StatusConnected,
	})

	if _, busy := srv.callOwner(authed(t, srv, asOperator("op-A")), ""); !busy {
		t.Fatal("op-A is already talking and must be refused")
	}
	if _, busy := srv.callOwner(authed(t, srv, asOperator("op-B")), ""); busy {
		t.Fatal("op-B is free and must be allowed")
	}
}

// An integration may reserve a call for a named operator.
func TestIntegrationCanTargetAnOperator(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	owner, busy := srv.callOwner(authed(t, srv, asIntegration()), "  op-B  ")
	if busy || owner != "op-B" {
		t.Fatalf("owner = %q busy = %v, want op-B and not busy", owner, busy)
	}
}

// Attaching audio is what claims a call: an unowned one becomes the attaching
// operator's, and after that nobody else can take it over.
func TestClaimingAnUnownedCall(t *testing.T) {
	srv, sess, h := newIntegrationServer(t)

	sess.reg.add("c1", &activeCall{})
	srv.broker.upsertCall(CallRecord{
		SessionID: sess.id, CallID: "c1", Owner: nil,
		Direction: "outbound", Peer: "5511999@s.whatsapp.net", Status: StatusRinging,
	})

	if !srv.broker.mayControl("c1", "op-A") {
		t.Fatal("an unowned call must be claimable by any operator")
	}
	if !srv.broker.setOwner("c1", "op-A") {
		t.Fatal("op-A should have been able to claim it")
	}
	srv.broker.rebroadcastCall("c1")

	rec, _ := srv.broker.getCall("c1")
	if rec.Owner == nil || *rec.Owner != "op-A" {
		t.Fatalf("after claiming, owner = %v, want op-A", rec.Owner)
	}
	if srv.broker.setOwner("c1", "op-B") {
		t.Fatal("a claimed call must not change hands")
	}

	// And a second browser is refused at the HTTP layer, before any SDP work.
	res := do(t, h, "POST",
		"http://example.com/api/sessions/"+sess.id+"/calls/c1/webrtc",
		asOperator("op-B"), `{"sdp_offer":"v=0"}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("second browser attaching: got %d, want 409", res.Code)
	}
}

func TestAPIKeyAuth(t *testing.T) {
	_, _, h := newIntegrationServer(t)
	path := "http://example.com/api/sessions"

	if res := do(t, h, "GET", path, nil, ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("no credential: got %d, want 401", res.Code)
	}
	wrong := map[string]string{"X-API-Key": "not-the-key"}
	if res := do(t, h, "GET", path, wrong, ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: got %d, want 401", res.Code)
	}
	if res := do(t, h, "GET", path, asIntegration(), ""); res.Code != http.StatusOK {
		t.Fatalf("api key: got %d, want 200", res.Code)
	}
	if res := do(t, h, "GET", path, asOperator("op-A"), ""); res.Code != http.StatusOK {
		t.Fatalf("operator token: got %d, want 200", res.Code)
	}
}

// With nothing configured the server stays open, which is how it has always
// behaved and what the local-only default relies on.
func TestNoCredentialsConfiguredStaysOpen(t *testing.T) {
	srv := newTestServerWithConfig(t, config{})
	res := do(t, srv.routes(), "GET", "http://example.com/api/sessions", nil, "")
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", res.Code)
	}
	if callerFrom(authed(t, srv, nil)).isIntegration() {
		t.Fatal("an unauthenticated caller is an operator, not an integration")
	}
}

func TestParseAPIKeys(t *testing.T) {
	keys, err := parseAPIKeys([]string{"crm:abc", " n8n : def "})
	if err != nil {
		t.Fatal(err)
	}
	if keys["crm"] != "abc" || keys["n8n"] != "def" {
		t.Fatalf("parsed wrong: %v", keys)
	}
	if name, ok := keys.match("def"); !ok || name != "n8n" {
		t.Fatalf("match(def) = %q, %v", name, ok)
	}
	if _, ok := keys.match(""); ok {
		t.Fatal("an empty credential must never match")
	}

	for _, bad := range []string{"noseparator", ":secret", "name:", ""} {
		if _, err := parseAPIKeys([]string{bad}); err == nil {
			t.Fatalf("%q should have been rejected", bad)
		}
	}
	if _, err := parseAPIKeys([]string{"crm:a", "crm:b"}); err == nil {
		t.Fatal("duplicate names should be rejected")
	}
}

// TestEndToEndIntegrationServer runs the whole thing the way another system sees
// it: the router with its real middleware over a real socket, credentials built
// the way the flags build them, and a webhook endpoint that has to receive a
// signed delivery.
func TestEndToEndIntegrationServer(t *testing.T) {
	hooks := newReceiver(t)

	keys, err := parseAPIKeys([]string{"crm:crm-secret", "n8n:n8n-secret"})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServerWithConfig(t, config{token: "operator-token", apiKeys: keys})
	srv.broker.webhooks = newWebhookSender(hooks.srv.URL, "hooksecret", slog.Default())
	t.Cleanup(srv.broker.closeWebhooks)

	api := httptest.NewServer(srv.routes())
	t.Cleanup(api.Close)

	get := func(t *testing.T, headers map[string]string) int {
		t.Helper()
		req, err := http.NewRequest("GET", api.URL+"/api/sessions", nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no credential", nil, http.StatusUnauthorized},
		{"wrong key", map[string]string{"X-API-Key": "nope"}, http.StatusUnauthorized},
		{"crm key", map[string]string{"X-API-Key": "crm-secret"}, http.StatusOK},
		{"n8n key", map[string]string{"X-API-Key": "n8n-secret"}, http.StatusOK},
		{"bearer", map[string]string{"Authorization": "Bearer crm-secret"}, http.StatusOK},
		{"operator token", map[string]string{"X-Auth-Token": "operator-token"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(t, tc.headers); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}

	// A call's lifecycle has to reach the other system, signed.
	srv.broker.upsertCall(CallRecord{
		SessionID: "s1", CallID: "c1", Direction: "outbound",
		Peer: "5511999@s.whatsapp.net", Status: StatusRinging, StartedAt: 1000,
	})
	srv.broker.endCall("c1", "user_ended")

	got := hooks.waitFor(t, 2)
	if got[0].Type != eventCallRinging || got[1].Type != eventCallEnded {
		t.Fatalf("events = %q, %q", got[0].Type, got[1].Type)
	}

	hooks.mu.Lock()
	body, header := hooks.bodies[0], hooks.headers[0]
	hooks.mu.Unlock()
	if header.Get("X-WaCalls-Signature") != signBody("hooksecret", body) {
		t.Fatal("the delivery was not signed with the configured secret")
	}
}

// duration_ms used to be accepted and ignored.
func TestDurationCapEndsTheCall(t *testing.T) {
	_, sess, _ := newIntegrationServer(t)

	sess.reg.add("c1", &activeCall{cm: newInertCallManager()})
	sess.capCallDuration("c1", 50*time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, live := sess.reg.get("c1"); !live {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the call outlived its duration_ms")
}

// A call that ends on its own must not leave a timer pointing at it.
func TestDurationCapStoppedWhenCallEndsFirst(t *testing.T) {
	_, sess, _ := newIntegrationServer(t)

	sess.reg.add("c1", &activeCall{cm: newInertCallManager()})
	sess.capCallDuration("c1", time.Hour)

	ac, ok := sess.reg.get("c1")
	if !ok || ac.cap == nil {
		t.Fatal("expected a duration timer on the call")
	}
	sess.removeCall("c1")
	if ac.cap.Stop() {
		t.Fatal("removeCall should have stopped the timer")
	}
}
