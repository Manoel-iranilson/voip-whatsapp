package main

import (
	"encoding/binary"
	"os"
	"sync"

	"wacalls/internal/voip/media"
)

// wavHeaderSize is the canonical 44-byte RIFF/WAVE header for uncompressed PCM.
const wavHeaderSize = 44

// wavWriter appends 16-bit little-endian PCM to a .wav file. Samples handed to
// Write are already interleaved for the declared channel count.
//
// The two size fields in the header are only knowable once the call ends, so the
// header goes out with zeros and Close seeks back to patch it. A file left behind
// by a crash therefore has zeroed sizes; most players still decode it, and the
// PCM payload is intact either way.
type wavWriter struct {
	sampleRate int
	channels   int

	mu     sync.Mutex
	f      *os.File
	n      uint32 // PCM bytes written so far
	broken bool   // a write failed; stop trying and stay quiet
	closed bool
}

func newWavWriter(path string, sampleRate, channels int) (*wavWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(wavHeader(sampleRate, channels, 0)); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return &wavWriter{sampleRate: sampleRate, channels: channels, f: f}, nil
}

// wavHeader builds a 16-bit PCM header for pcmBytes of audio data.
func wavHeader(sampleRate, channels int, pcmBytes uint32) []byte {
	const bitsPerSample = 16

	byteRate := uint32(sampleRate * channels * bitsPerSample / 8)
	blockAlign := uint16(channels * bitsPerSample / 8)

	h := make([]byte, wavHeaderSize)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+pcmBytes) // RIFF chunk size
	copy(h[8:], "WAVE")

	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(h[20:], 1)  // PCM, uncompressed
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:], byteRate)
	binary.LittleEndian.PutUint16(h[32:], blockAlign)
	binary.LittleEndian.PutUint16(h[34:], bitsPerSample)

	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], pcmBytes) // data chunk size
	return h
}

// Write appends one frame. It returns the first write error and then goes inert,
// so a full disk does not produce a log line every 20 ms.
func (w *wavWriter) Write(pcm []float32) error {
	if len(pcm) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.broken || w.closed {
		return nil
	}
	n, err := w.f.Write(media.PCMFloat32ToInt16LE(pcm))
	w.n += uint32(n)
	if err != nil {
		w.broken = true
		return err
	}
	return nil
}

// Close patches the header sizes and closes the file.
func (w *wavWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true

	var patchErr error
	if !w.broken {
		if _, err := w.f.WriteAt(wavHeader(w.sampleRate, w.channels, w.n), 0); err != nil {
			patchErr = err
		}
	}
	if err := w.f.Close(); err != nil && patchErr == nil {
		patchErr = err
	}
	return patchErr
}

// Bytes reports how much PCM has been written, for tests and logging.
func (w *wavWriter) Bytes() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}
