package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Event names delivered to -webhook-url. They mirror the call lifecycle the SSE
// stream already reports, named so another system can switch on them.
const (
	eventCallIncoming = "call.incoming"
	eventCallRinging  = "call.ringing"
	eventCallActive   = "call.active"
	eventCallEnded    = "call.ended"
)

const (
	webhookQueueSize = 256
	webhookAttempts  = 3
	webhookTimeout   = 10 * time.Second
	// webhookDrainTimeout bounds how long shutdown waits for queued deliveries.
	// Without it a slow endpoint and a full queue could hold the process open for
	// the better part of an hour.
	webhookDrainTimeout = 5 * time.Second
)

// webhookEvent is the JSON body POSTed to the configured endpoint.
type webhookEvent struct {
	Type      string  `json:"type"`
	Delivery  string  `json:"delivery"`
	SentAt    int64   `json:"sentAt"`
	SessionID string  `json:"sessionId"`
	CallID    string  `json:"callId"`
	Direction string  `json:"direction"`
	Peer      string  `json:"peer"`
	Status    string  `json:"status"`
	Owner     *string `json:"owner"`
	StartedAt int64   `json:"startedAt"`
	EndedAt   *int64  `json:"endedAt,omitempty"`
	EndReason string  `json:"endReason,omitempty"`
	Recording string  `json:"recording,omitempty"`
}

// webhookSender delivers events to another system.
//
// Delivery happens on its own goroutine behind a bounded queue: a slow or dead
// endpoint must never hold up a call. When the queue is full events are dropped
// rather than queued forever — a webhook is a notification, not a ledger.
type webhookSender struct {
	url    string
	secret string
	log    *slog.Logger
	client *http.Client

	queue chan webhookEvent

	once     sync.Once
	done     chan struct{}
	mu       sync.Mutex
	dropped  int
	reported bool
}

func newWebhookSender(url, secret string, log *slog.Logger) *webhookSender {
	s := &webhookSender{
		url:    url,
		secret: secret,
		log:    log,
		client: &http.Client{Timeout: webhookTimeout},
		queue:  make(chan webhookEvent, webhookQueueSize),
		done:   make(chan struct{}),
	}
	go s.run()
	return s
}

// send queues an event. It never blocks.
func (s *webhookSender) send(ev webhookEvent) {
	if s == nil {
		return
	}
	ev.Delivery = newDeliveryID()
	ev.SentAt = time.Now().UnixMilli()
	select {
	case s.queue <- ev:
	default:
		s.mu.Lock()
		s.dropped++
		first := !s.reported
		s.reported = true
		s.mu.Unlock()
		if first {
			s.log.Warn("webhook endpoint is not keeping up; dropping events", "url", s.url)
		}
	}
}

func (s *webhookSender) run() {
	defer close(s.done)
	for ev := range s.queue {
		s.deliver(ev)
	}
}

func (s *webhookSender) deliver(ev webhookEvent) {
	body, err := json.Marshal(ev)
	if err != nil {
		s.log.Error("could not encode webhook", "err", err)
		return
	}

	backoff := 500 * time.Millisecond
	for attempt := 1; attempt <= webhookAttempts; attempt++ {
		status, err := s.post(body, ev)
		if err == nil && status >= 200 && status < 300 {
			return
		}
		// A 4xx other than 429 means the endpoint rejected the payload itself;
		// repeating it will not help.
		if err == nil && status >= 400 && status < 500 && status != http.StatusTooManyRequests {
			s.log.Warn("webhook rejected", "type", ev.Type, "call_id", ev.CallID, "status", status)
			return
		}
		if attempt < webhookAttempts {
			time.Sleep(backoff)
			backoff *= 2
		} else {
			s.log.Warn("webhook delivery failed, giving up",
				"type", ev.Type, "call_id", ev.CallID, "status", status, "err", err)
		}
	}
}

func (s *webhookSender) post(body []byte, ev webhookEvent) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wacalls-webhook")
	req.Header.Set("X-WaCalls-Event", ev.Type)
	req.Header.Set("X-WaCalls-Delivery", ev.Delivery)
	if s.secret != "" {
		req.Header.Set("X-WaCalls-Signature", signBody(s.secret, body))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// Close stops accepting events and gives the queued ones a bounded window to go
// out. Shutdown must not hang because the other end is slow.
func (s *webhookSender) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.queue)
		select {
		case <-s.done:
		case <-time.After(webhookDrainTimeout):
			s.log.Warn("webhook queue still draining at shutdown; dropping the rest", "url", s.url)
		}
	})
}

// signBody is the HMAC-SHA256 of the raw body, in the "sha256=<hex>" shape that
// GitHub and Stripe use, so the receiving end can verify it with any library.
func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newDeliveryID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// webhookEventFor turns a call record into an event, or reports false when this
// status is not one worth notifying about.
func webhookEventFor(r CallRecord) (webhookEvent, bool) {
	var name string
	switch r.Status {
	case StatusRinging:
		name = eventCallRinging
	case StatusConnected:
		name = eventCallActive
	case StatusEnded:
		name = eventCallEnded
	default:
		return webhookEvent{}, false
	}
	return webhookEvent{
		Type:      name,
		SessionID: r.SessionID,
		CallID:    r.CallID,
		Direction: r.Direction,
		Peer:      r.Peer,
		Status:    string(r.Status),
		Owner:     r.Owner,
		StartedAt: r.StartedAt,
		EndedAt:   r.EndedAt,
		EndReason: r.EndReason,
		Recording: r.Recording,
	}, true
}
