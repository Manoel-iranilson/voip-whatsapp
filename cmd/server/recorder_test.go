package main

import (
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wacalls/internal/voip/core"
)

// A nil recorder is the "recording off" state, and the audio hot paths call it
// without checking, so every method has to tolerate it.
func TestNilRecorderIsInert(t *testing.T) {
	var r *callRecorder
	r.WriteLocal([]float32{0.1, 0.2})
	r.WritePeer([]float32{0.3})
	r.Close()
	if got := r.Base(); got != "" {
		t.Fatalf("nil recorder Base() = %q, want empty", got)
	}
}

func TestNewCallRecorderDisabledWithoutDir(t *testing.T) {
	r, err := newCallRecorder("", "sess", "call", slog.Default())
	if err != nil {
		t.Fatalf("empty dir should not be an error, got %v", err)
	}
	if r != nil {
		t.Fatal("empty dir must yield a nil recorder")
	}
}

// readFrames returns the recording as [frame][channel] int16 samples.
func readFrames(t *testing.T, dir, session, base string) [][2]int16 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, session, base+".wav"))
	if err != nil {
		t.Fatal(err)
	}
	pcm := b[wavHeaderSize:]
	out := make([][2]int16, len(pcm)/4)
	for i := range out {
		out[i][0] = int16(binary.LittleEndian.Uint16(pcm[i*4:]))
		out[i][1] = int16(binary.LittleEndian.Uint16(pcm[i*4+2:]))
	}
	return out
}

func loudFrame(v float32) []float32 {
	f := make([]float32, chunk)
	for i := range f {
		f[i] = v
	}
	return f
}

const chunk = 320 // core.DefaultAudioConfig.CaptureChunkSize

// This is the test the whole change exists for. The two legs arrive on different
// goroutines at different moments; only a shared clock puts them at the same
// place on the timeline. Driving flushFrame by hand keeps it deterministic — a
// sleep-based version of this test would be flaky.
func TestCallRecorderAlignsTheTwoLegs(t *testing.T) {
	dir := t.TempDir()
	r, err := newCallRecorder(dir, "sess-1", "CALL123", slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// Only the operator speaks during the first frame...
	r.WriteLocal(loudFrame(0.5))
	r.flushFrame()
	// ...and only the remote party during the second.
	r.WritePeer(loudFrame(-0.5))
	r.flushFrame()
	r.Close()

	frames := readFrames(t, dir, "sess-1", r.Base())
	if len(frames) < 2*chunk {
		t.Fatalf("expected at least %d stereo frames, got %d", 2*chunk, len(frames))
	}

	first, second := frames[chunk/2], frames[chunk+chunk/2]
	if first[channelLocal] == 0 {
		t.Fatal("frame 1: the operator spoke, left channel should carry audio")
	}
	if first[channelPeer] != 0 {
		t.Fatalf("frame 1: the remote party was silent, right channel should be too, got %d", first[channelPeer])
	}
	if second[channelPeer] == 0 {
		t.Fatal("frame 2: the remote party spoke, right channel should carry audio")
	}
	if second[channelLocal] != 0 {
		t.Fatalf("frame 2: the operator was silent, left channel should be too, got %d", second[channelLocal])
	}
}

func TestCallRecorderProducesOneStereoFile(t *testing.T) {
	dir := t.TempDir()
	r, err := newCallRecorder(dir, "sess-1", "CALL123", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	r.WriteLocal(loudFrame(0.5))
	r.WritePeer(loudFrame(-0.5))
	r.flushFrame()
	r.Close()

	base := r.Base()
	if !strings.HasSuffix(base, "-CALL123") {
		t.Fatalf("base %q should end with the call id", base)
	}

	// Exactly one file, and it is stereo.
	entries, err := os.ReadDir(filepath.Join(dir, "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != base+".wav" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("expected a single %q, got %v", base+".wav", names)
	}

	header, err := os.ReadFile(filepath.Join(dir, "sess-1", base+".wav"))
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint16(header[22:]); got != 2 {
		t.Fatalf("channels = %d, want 2", got)
	}

	frames := readFrames(t, dir, "sess-1", base)
	mid := frames[chunk/2]
	if mid[channelLocal] <= 0 {
		t.Fatalf("operator should be positive on the left, got %d", mid[channelLocal])
	}
	if mid[channelPeer] >= 0 {
		t.Fatalf("remote party should be negative on the right, got %d", mid[channelPeer])
	}
}

// A call where only one side ever sends still yields a normal stereo file; the
// other channel is simply silent.
func TestCallRecorderOneSidedCall(t *testing.T) {
	dir := t.TempDir()
	r, err := newCallRecorder(dir, "sess-1", "SOLO", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	r.WritePeer(loudFrame(0.75))
	r.flushFrame()
	r.Close()

	for _, f := range readFrames(t, dir, "sess-1", r.Base()) {
		if f[channelLocal] != 0 {
			t.Fatalf("left channel should be silent throughout, got %d", f[channelLocal])
		}
	}
}

// A call that never carried audio (rang and was rejected) must not produce
// minutes of silence just because the recorder existed.
func TestCallRecorderSilentCallStaysEmpty(t *testing.T) {
	dir := t.TempDir()
	r, err := newCallRecorder(dir, "sess-1", "RANG", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	r.flushFrame()
	r.flushFrame()
	r.Close()

	if got := fileSize(t, filepath.Join(dir, "sess-1", r.Base()+".wav")); got != wavHeaderSize {
		t.Fatalf("file is %d bytes, want just the %d-byte header", got, wavHeaderSize)
	}
}

// The browser's clock and this process's clock differ, so one side can run ahead
// indefinitely. The queue must stay bounded rather than grow for the whole call.
func TestCallRecorderBoundsBacklog(t *testing.T) {
	r, err := newCallRecorder(t.TempDir(), "sess", "FLOOD", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Ten seconds of audio with nothing draining it.
	for range 10 * core.DefaultAudioConfig.SampleRate / chunk {
		r.WriteLocal(loudFrame(0.1))
	}

	r.mu.Lock()
	queued := len(r.local)
	r.mu.Unlock()
	if queued > maxBacklog {
		t.Fatalf("queue grew to %d samples, cap is %d", queued, maxBacklog)
	}
	if !r.overflowed {
		t.Fatal("overflowing the queue should have been reported")
	}
}

func TestCallRecorderCloseIsIdempotent(t *testing.T) {
	r, err := newCallRecorder(t.TempDir(), "sess", "call", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	r.Close()
	// Writes after Close must be dropped, not panic on a closed file.
	r.WriteLocal(make([]float32, 320))
	r.WritePeer(make([]float32, 320))
}

// The call id of an inbound call is the `call-id` attribute taken straight from
// the peer's <call> stanza, so it must never be able to steer the write outside
// the recording directory.
func TestCallRecorderRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	hostile := []string{
		`../../../evil`,
		`..\..\evil`,
		`/etc/passwd`,
		`a/b/c`,
		`..`,
	}

	for _, callID := range hostile {
		r, err := newCallRecorder(dir, "sess", callID, slog.Default())
		if err != nil {
			t.Fatalf("callID %q: %v", callID, err)
		}
		r.WriteLocal(make([]float32, 8))
		r.Close()

		base := r.Base()
		for _, bad := range []string{"/", `\`, ".."} {
			if strings.Contains(base, bad) {
				t.Fatalf("callID %q produced unsafe base %q", callID, base)
			}
		}
	}

	// Everything written must live directly under <dir>/sess.
	root := filepath.Join(dir, "sess")
	var found int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		found++
		if filepath.Dir(path) != root {
			t.Fatalf("file escaped to %q, expected everything under %q", path, root)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found != len(hostile) {
		t.Fatalf("found %d files, want %d (one per call)", found, len(hostile))
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"ABC123":                 "ABC123",
		"a-b_c":                  "a-b_c",
		"../evil":                "___evil",
		"a/b":                    "a_b",
		`a\b`:                    "a_b",
		"..":                     "__",
		"":                       "unnamed",
		strings.Repeat("x", 200): strings.Repeat("x", maxNameSegment),
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Fatalf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
