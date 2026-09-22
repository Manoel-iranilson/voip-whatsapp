package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wacalls/internal/voip/core"
)

// The recording is one stereo file: the operator on the left channel, the remote
// party on the right. That plays back as a single conversation while still
// letting the two voices be separated afterwards.
const (
	recordingChannels = 2
	channelLocal      = 0
	channelPeer       = 1
)

// maxNameSegment bounds a sanitized path segment so a hostile call-id cannot
// blow past the filesystem's name limit.
const maxNameSegment = 64

// maxBacklog caps how far ahead of the mixer either side may run, at two seconds
// of audio. The browser's AudioContext and this process do not share a clock, so
// over a long call one side drifts ahead; past the cap the oldest samples are
// dropped, which keeps the recording anchored to the present instead of falling
// progressively behind.
var maxBacklog = 2 * core.DefaultAudioConfig.SampleRate

// callRecorder mixes both legs of one call into a single stereo WAV.
//
// WriteLocal and WritePeer only enqueue; a goroutine drains both queues on a
// fixed tick and pads whichever side came up short with silence. That shared
// clock is what keeps the two channels on the same timeline — the legs arrive on
// different goroutines, at different rates, starting at different moments.
//
// Every method tolerates a nil receiver, so the audio hot paths can call it
// unconditionally: recording off simply means the pointer is nil.
type callRecorder struct {
	base string // file name stem, also the on-disk name
	log  *slog.Logger

	mu         sync.Mutex
	local      []float32 // samples waiting to be mixed, operator side
	peer       []float32 // samples waiting to be mixed, remote side
	started    bool      // audio has arrived from at least one side
	closed     bool
	overflowed bool // a queue hit the cap; say so once
	warned     bool // a write failed; say so once

	out  *wavWriter
	stop chan struct{}
	done chan struct{}
}

// newCallRecorder creates <dir>/<sessionID>/<millis>-<callID>.wav and starts the
// mixer.
//
// callID is not ours to trust on inbound calls: it is the `call-id` attribute
// lifted straight from the peer's <call> stanza, so it is sanitized before it
// ever reaches the filesystem.
func newCallRecorder(dir, sessionID, callID string, log *slog.Logger) (*callRecorder, error) {
	if dir == "" {
		return nil, nil
	}
	sessionDir := filepath.Join(dir, safeName(sessionID))
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("create recording dir: %w", err)
	}

	base := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), safeName(callID))
	out, err := newWavWriter(
		filepath.Join(sessionDir, base+".wav"),
		core.DefaultAudioConfig.SampleRate,
		recordingChannels,
	)
	if err != nil {
		return nil, err
	}

	r := &callRecorder{
		base: base,
		log:  log,
		out:  out,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go r.mix()

	log.Info("recording call", "call_id", callID, "base", base)
	return r, nil
}

// Base is the file name stem, stored in the history row so a recording can be
// found again after the call is gone.
func (r *callRecorder) Base() string {
	if r == nil {
		return ""
	}
	return r.base
}

func (r *callRecorder) WriteLocal(pcm []float32) { r.enqueue(channelLocal, pcm) }
func (r *callRecorder) WritePeer(pcm []float32)  { r.enqueue(channelPeer, pcm) }

func (r *callRecorder) enqueue(channel int, pcm []float32) {
	if r == nil || len(pcm) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}

	// The timeline starts at the first audio, not at call setup: a call that rings
	// for half a minute should not open with half a minute of silence.
	r.started = true

	queue := &r.local
	if channel == channelPeer {
		queue = &r.peer
	}
	*queue = append(*queue, pcm...)

	if excess := len(*queue) - maxBacklog; excess > 0 {
		*queue = append((*queue)[:0], (*queue)[excess:]...)
		if !r.overflowed {
			r.overflowed = true
			r.log.Warn("recording fell behind; dropping the oldest audio to stay in sync",
				"base", r.base)
		}
	}
}

// mix drains both queues on a fixed tick until Close stops it.
func (r *callRecorder) mix() {
	defer close(r.done)
	ticker := time.NewTicker(time.Duration(core.DefaultAudioConfig.IntervalMs) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.flushFrame()
		}
	}
}

// flushFrame emits one interleaved frame, padding whichever side came up short
// with silence. The padding is the whole point: it holds both channels at the
// same position on the timeline even when only one side is sending.
//
// It reports whether there was anything left to flush, which Close uses to drain.
func (r *callRecorder) flushFrame() bool {
	size := core.DefaultAudioConfig.CaptureChunkSize

	r.mu.Lock()
	pending := len(r.local) > 0 || len(r.peer) > 0
	// Nothing has been heard yet, or the call is over and the queues are empty:
	// writing more silence would only pad the file.
	if !r.started || (r.closed && !pending) {
		r.mu.Unlock()
		return false
	}
	left := takeSamples(&r.local, size)
	right := takeSamples(&r.peer, size)
	r.mu.Unlock()

	frame := make([]float32, size*recordingChannels)
	for i := range size {
		if i < len(left) {
			frame[i*recordingChannels+channelLocal] = left[i]
		}
		if i < len(right) {
			frame[i*recordingChannels+channelPeer] = right[i]
		}
	}

	if err := r.out.Write(frame); err != nil {
		r.mu.Lock()
		first := !r.warned
		r.warned = true
		r.mu.Unlock()
		if first {
			r.log.Error("recording write failed; this call will be incomplete",
				"base", r.base, "err", err)
		}
	}
	return pending
}

// takeSamples removes up to n samples from the front of a queue.
func takeSamples(queue *[]float32, n int) []float32 {
	if len(*queue) == 0 {
		return nil
	}
	n = min(n, len(*queue))
	out := make([]float32, n)
	copy(out, (*queue)[:n])
	*queue = append((*queue)[:0], (*queue)[n:]...)
	return out
}

// Close stops the mixer, flushes whatever is still queued and finishes the file.
func (r *callRecorder) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()

	close(r.stop)
	<-r.done

	// Drain what the last tick did not reach. The backlog cap bounds this to
	// roughly a hundred frames, so it cannot hang shutdown.
	for r.flushFrame() {
	}

	if err := r.out.Close(); err != nil {
		r.log.Error("closing recording failed", "base", r.base, "err", err)
	}
}

// safeName reduces an identifier to something that can only ever name a single
// file inside its directory: no separators, no dot-dot, no surprises.
func safeName(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= maxNameSegment {
			break
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}
