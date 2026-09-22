package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// receiver stands in for the other system's endpoint.
type receiver struct {
	srv  *httptest.Server
	stop chan struct{} // released at cleanup so a slow handler never stalls teardown

	mu       sync.Mutex
	events   []webhookEvent
	bodies   [][]byte
	headers  []http.Header
	status   int
	delay    time.Duration
	attempts int
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{status: http.StatusOK, stop: make(chan struct{})}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		r.mu.Lock()
		delay, status := r.delay, r.status
		r.attempts++
		var ev webhookEvent
		if json.Unmarshal(body, &ev) == nil {
			r.events = append(r.events, ev)
		}
		r.bodies = append(r.bodies, body)
		r.headers = append(r.headers, req.Header.Clone())
		r.mu.Unlock()

		if delay > 0 {
			// httptest.Server.Close waits for handlers to return, so this sleep
			// has to be interruptible or every slow-endpoint test pays for it.
			select {
			case <-time.After(delay):
			case <-r.stop:
			}
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(func() {
		close(r.stop)
		r.srv.Close()
	})
	return r
}

func (r *receiver) received() []webhookEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]webhookEvent(nil), r.events...)
}

func (r *receiver) waitFor(t *testing.T, n int) []webhookEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.received(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d webhook deliveries, got %d", n, len(r.received()))
	return nil
}

func brokerWithWebhooks(t *testing.T, rec *receiver, secret string) *Broker {
	t.Helper()
	b := NewBroker()
	b.webhooks = newWebhookSender(rec.srv.URL, secret, slog.Default())
	t.Cleanup(b.webhooks.Close)
	return b
}

func TestWebhookDeliversCallLifecycle(t *testing.T) {
	rec := newReceiver(t)
	b := brokerWithWebhooks(t, rec, "")

	b.upsertCall(CallRecord{
		SessionID: "s1", CallID: "c1", Direction: "outbound",
		Peer: "5511999@s.whatsapp.net", Status: StatusRinging, StartedAt: 1000,
	})
	b.upsertCall(CallRecord{
		SessionID: "s1", CallID: "c1", Direction: "outbound",
		Peer: "5511999@s.whatsapp.net", Status: StatusConnected, StartedAt: 1000,
	})
	b.endCall("c1", "user_ended")

	got := rec.waitFor(t, 3)
	want := []string{eventCallRinging, eventCallActive, eventCallEnded}
	for i, name := range want {
		if got[i].Type != name {
			t.Fatalf("delivery %d = %q, want %q", i, got[i].Type, name)
		}
		if got[i].CallID != "c1" || got[i].SessionID != "s1" {
			t.Fatalf("delivery %d lost its identifiers: %+v", i, got[i])
		}
		if got[i].Delivery == "" {
			t.Fatalf("delivery %d has no id", i)
		}
	}
	if got[2].EndReason != "user_ended" || got[2].EndedAt == nil {
		t.Fatalf("the ended event should carry the reason and timestamp: %+v", got[2])
	}
}

// upsertCall fires on every transition, several of which keep the same status.
// Consumers want the change, not the churn.
func TestWebhookSkipsUnchangedStatus(t *testing.T) {
	rec := newReceiver(t)
	b := brokerWithWebhooks(t, rec, "")

	for range 5 {
		b.upsertCall(CallRecord{
			SessionID: "s1", CallID: "c1", Direction: "outbound",
			Peer: "x", Status: StatusRinging, StartedAt: 1000,
		})
	}
	b.upsertCall(CallRecord{
		SessionID: "s1", CallID: "c1", Direction: "outbound",
		Peer: "x", Status: StatusConnected, StartedAt: 1000,
	})

	got := rec.waitFor(t, 2)
	time.Sleep(200 * time.Millisecond) // give any extra deliveries a chance to arrive
	if got = rec.received(); len(got) != 2 {
		types := make([]string, len(got))
		for i, e := range got {
			types[i] = e.Type
		}
		t.Fatalf("expected exactly ringing then active, got %v", types)
	}
}

func TestWebhookSignature(t *testing.T) {
	rec := newReceiver(t)
	b := brokerWithWebhooks(t, rec, "topsecret")

	b.upsertCall(CallRecord{
		SessionID: "s1", CallID: "c1", Direction: "outbound",
		Peer: "x", Status: StatusRinging, StartedAt: 1000,
	})
	rec.waitFor(t, 1)

	rec.mu.Lock()
	body, header := rec.bodies[0], rec.headers[0]
	rec.mu.Unlock()

	if got, want := header.Get("X-WaCalls-Signature"), signBody("topsecret", body); got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
	if signBody("wrongsecret", body) == header.Get("X-WaCalls-Signature") {
		t.Fatal("a different secret must produce a different signature")
	}
	if header.Get("X-WaCalls-Event") != eventCallRinging {
		t.Fatalf("event header = %q", header.Get("X-WaCalls-Event"))
	}
	if header.Get("X-WaCalls-Delivery") == "" {
		t.Fatal("delivery header missing")
	}
}

func TestWebhookUnsignedWhenNoSecret(t *testing.T) {
	rec := newReceiver(t)
	b := brokerWithWebhooks(t, rec, "")

	b.upsertCall(CallRecord{SessionID: "s1", CallID: "c1", Peer: "x", Status: StatusRinging})
	rec.waitFor(t, 1)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if sig := rec.headers[0].Get("X-WaCalls-Signature"); sig != "" {
		t.Fatalf("no secret configured, but got signature %q", sig)
	}
}

// The call path must never wait on the other system. This is the property that
// keeps a dead endpoint from freezing calls.
func TestWebhookDoesNotBlockTheCallPath(t *testing.T) {
	rec := newReceiver(t)
	rec.mu.Lock()
	rec.delay = 2 * time.Second
	rec.mu.Unlock()

	b := brokerWithWebhooks(t, rec, "")

	start := time.Now()
	for i := range 20 {
		b.upsertCall(CallRecord{
			SessionID: "s1", CallID: "c1", Peer: "x",
			Status: StatusRinging, StartedAt: int64(i),
		})
		b.upsertCall(CallRecord{
			SessionID: "s1", CallID: "c1", Peer: "x",
			Status: StatusConnected, StartedAt: int64(i),
		})
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a slow endpoint held up the call path for %v", elapsed)
	}

	// Let the queue drain quickly now that the point is made, so cleanup does not
	// sit through forty slow deliveries.
	rec.mu.Lock()
	rec.delay = 0
	rec.mu.Unlock()
}

// Shutdown must not wait out a slow endpoint.
func TestWebhookCloseIsBounded(t *testing.T) {
	rec := newReceiver(t)
	rec.mu.Lock()
	rec.delay = webhookTimeout
	rec.mu.Unlock()

	sender := newWebhookSender(rec.srv.URL, "", slog.Default())
	for range 20 {
		sender.send(webhookEvent{Type: eventCallRinging, CallID: "c1"})
	}

	start := time.Now()
	sender.Close()
	if elapsed := time.Since(start); elapsed > webhookDrainTimeout+2*time.Second {
		t.Fatalf("Close took %v; it must give up after about %v", elapsed, webhookDrainTimeout)
	}
}

// A 4xx means the endpoint rejected the payload; repeating it will not help.
func TestWebhookDoesNotRetryClientErrors(t *testing.T) {
	rec := newReceiver(t)
	rec.mu.Lock()
	rec.status = http.StatusBadRequest
	rec.mu.Unlock()

	b := brokerWithWebhooks(t, rec, "")
	b.upsertCall(CallRecord{SessionID: "s1", CallID: "c1", Peer: "x", Status: StatusRinging})
	rec.waitFor(t, 1)
	time.Sleep(300 * time.Millisecond)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.attempts != 1 {
		t.Fatalf("a 400 was attempted %d times, want 1", rec.attempts)
	}
}

// A nil sender is the "webhooks off" state and has to be inert.
func TestNilWebhookSenderIsInert(t *testing.T) {
	var s *webhookSender
	s.send(webhookEvent{Type: eventCallRinging})
	s.Close()

	b := NewBroker()
	b.upsertCall(CallRecord{SessionID: "s1", CallID: "c1", Peer: "x", Status: StatusRinging})
	b.endCall("c1", "done")
	b.closeWebhooks()
}

func TestWebhookEventForIgnoresUninterestingStatuses(t *testing.T) {
	if _, ok := webhookEventFor(CallRecord{Status: StatusStarting}); ok {
		t.Fatal("'starting' is an internal step, not a webhook event")
	}
	for _, s := range []CallStatus{StatusRinging, StatusConnected, StatusEnded} {
		if _, ok := webhookEventFor(CallRecord{Status: s}); !ok {
			t.Fatalf("%q should produce an event", s)
		}
	}
}
