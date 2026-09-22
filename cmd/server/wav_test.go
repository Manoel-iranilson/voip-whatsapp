package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func readWav(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < wavHeaderSize {
		t.Fatalf("file is %d bytes, shorter than a WAV header", len(b))
	}
	return b
}

func TestWavWriterStereoHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stereo.wav")
	w, err := newWavWriter(path, 16000, 2)
	if err != nil {
		t.Fatal(err)
	}
	// One interleaved frame of 4 sample pairs.
	if err := w.Write([]float32{0.5, -0.5, 0.5, -0.5, 0.5, -0.5, 0.5, -0.5}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b := readWav(t, path)
	if got := binary.LittleEndian.Uint16(b[22:]); got != 2 {
		t.Fatalf("channels = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint32(b[28:]); got != 64000 {
		t.Fatalf("byte rate = %d, want 64000 (16000 * 2ch * 2 bytes)", got)
	}
	if got := binary.LittleEndian.Uint16(b[32:]); got != 4 {
		t.Fatalf("block align = %d, want 4 (2ch * 2 bytes)", got)
	}
	if got := binary.LittleEndian.Uint32(b[40:]); got != 16 {
		t.Fatalf("data size = %d, want 16", got)
	}

	// Interleaving survives the round trip: left positive, right negative.
	pcm := b[wavHeaderSize:]
	for i := range 4 {
		left := int16(binary.LittleEndian.Uint16(pcm[i*4:]))
		right := int16(binary.LittleEndian.Uint16(pcm[i*4+2:]))
		if left <= 0 || right >= 0 {
			t.Fatalf("pair %d: left=%d right=%d, want left positive and right negative", i, left, right)
		}
	}
}

func TestWavWriterHeaderAndSizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.wav")
	w, err := newWavWriter(path, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}

	// 3 frames of 320 samples: 960 samples -> 1920 bytes of PCM.
	frame := make([]float32, 320)
	for i := range frame {
		frame[i] = 0.5
	}
	for range 3 {
		if err := w.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if got := w.Bytes(); got != 1920 {
		t.Fatalf("wrote %d PCM bytes, want 1920", got)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b := readWav(t, path)
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE file: %q %q", b[0:4], b[8:12])
	}
	if string(b[12:16]) != "fmt " || string(b[36:40]) != "data" {
		t.Fatalf("missing fmt/data chunks: %q %q", b[12:16], b[36:40])
	}
	if got := binary.LittleEndian.Uint16(b[20:]); got != 1 {
		t.Fatalf("audio format = %d, want 1 (PCM)", got)
	}
	if got := binary.LittleEndian.Uint16(b[22:]); got != 1 {
		t.Fatalf("channels = %d, want 1 (mono)", got)
	}
	if got := binary.LittleEndian.Uint32(b[24:]); got != 16000 {
		t.Fatalf("sample rate = %d, want 16000", got)
	}
	if got := binary.LittleEndian.Uint16(b[34:]); got != 16 {
		t.Fatalf("bits per sample = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(b[28:]); got != 32000 {
		t.Fatalf("byte rate = %d, want 32000", got)
	}

	// The two size fields are the ones Close has to patch.
	if got := binary.LittleEndian.Uint32(b[40:]); got != 1920 {
		t.Fatalf("data chunk size = %d, want 1920", got)
	}
	if got := binary.LittleEndian.Uint32(b[4:]); got != 36+1920 {
		t.Fatalf("RIFF chunk size = %d, want %d", got, 36+1920)
	}
	if len(b) != wavHeaderSize+1920 {
		t.Fatalf("file is %d bytes, want %d", len(b), wavHeaderSize+1920)
	}
}

func TestWavWriterRoundTripsSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "round.wav")
	w, err := newWavWriter(path, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]float32{0, 0.5, -0.5, 1, -1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	pcm := readWav(t, path)[wavHeaderSize:]
	got := make([]int16, len(pcm)/2)
	for i := range got {
		got[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
	}
	want := []int16{0, 16383, -16383, 32767, -32768}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		// Rounding differs by at most one step between encoders.
		if d := int(got[i]) - int(want[i]); d > 1 || d < -1 {
			t.Fatalf("sample %d = %d, want ~%d", i, got[i], want[i])
		}
	}
}

func TestWavWriterCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.wav")
	w, err := newWavWriter(path, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close should be a no-op, got %v", err)
	}
	// Writing after Close must not panic or reopen the file.
	if err := w.Write([]float32{0.1}); err != nil {
		t.Fatalf("write after close should be inert, got %v", err)
	}
	if got := len(readWav(t, path)); got != wavHeaderSize {
		t.Fatalf("file grew after Close: %d bytes", got)
	}
}

func TestWavWriterEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.wav")
	w, err := newWavWriter(path, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b := readWav(t, path)
	if got := binary.LittleEndian.Uint32(b[40:]); got != 0 {
		t.Fatalf("data size = %d, want 0 for a call with no audio", got)
	}
	if got := binary.LittleEndian.Uint32(b[4:]); got != 36 {
		t.Fatalf("RIFF size = %d, want 36", got)
	}
}
