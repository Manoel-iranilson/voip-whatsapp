// Receives 16 kHz PCM frames and plays them at the context's native rate
// (44.1/48 kHz) with linear interpolation. See capture-processor.js for why
// the context does not run at 16 kHz.
//
// Frames arrive in bursts (network jitter, SCTP retransmits, a busy main
// thread on iOS). Playing each sample as soon as it lands turns every late
// frame into a gap, which is the choppy audio heard on iPhone. So this is a
// small adaptive jitter buffer:
// - it holds back `target` samples before starting, and again after a gap;
// - every gap grows `target` a little (up to MAX_TARGET), and it shrinks back
//   slowly while playback stays smooth;
// - when the buffer runs well above target (sender clock faster than ours) it
//   plays ~2% faster to drain it instead of letting latency pile up.
const DEFAULT_SOURCE_RATE = 16000;
// Two seconds of 16 kHz audio.
const RING_SIZE = DEFAULT_SOURCE_RATE * 2;
const MS = DEFAULT_SOURCE_RATE / 1000;
const MIN_TARGET = 60 * MS;
const START_TARGET = 100 * MS;
const MAX_TARGET = 300 * MS;
const GROW_STEP = 40 * MS;
// Shrink by 10 ms after this many seconds without a gap.
const SHRINK_AFTER_S = 10;
const SHRINK_STEP = 10 * MS;
// Samples (at output rate) to fade over when a gap starts, to avoid a click.
const FADE = 64;

class PlaybackProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const opts = (options && options.processorOptions) || {};
    const source = opts.sourceRate > 0 ? opts.sourceRate : DEFAULT_SOURCE_RATE;
    // Source samples advanced per output sample (1/3 at 48 kHz).
    this.step = source / sampleRate;
    this.ring = new Float32Array(RING_SIZE);
    this.read = 0;
    this.write = 0;
    this.available = 0;
    // Fractional position between ring[read] and ring[read + 1].
    this.frac = 0;
    this.target = START_TARGET;
    this.playing = false;
    this.smoothSamples = 0;
    this.last = 0;
    this.fade = 0;
    this.closed = false;
    // Diagnostics, reported to the page once a second.
    this.underruns = 0;
    this.minLevel = Infinity;
    this.statsAt = 0;
    this.port.onmessage = (e) => {
      const data = e.data;
      if (data === "close") {
        this.closed = true;
        return;
      }
      for (let i = 0; i < data.length; i += 1) {
        this.ring[this.write] = data[i];
        this.write = (this.write + 1) % RING_SIZE;
        if (this.available < RING_SIZE) {
          this.available += 1;
        } else {
          this.read = (this.read + 1) % RING_SIZE;
        }
      }
    };
  }

  underrun() {
    this.playing = false;
    this.fade = FADE;
    this.smoothSamples = 0;
    this.target = Math.min(MAX_TARGET, this.target + GROW_STEP);
    this.underruns += 1;
  }

  report(frames) {
    if (this.playing) this.minLevel = Math.min(this.minLevel, this.available);
    this.statsAt += frames;
    if (this.statsAt < sampleRate) return;
    this.statsAt = 0;
    this.port.postMessage({
      underruns: this.underruns,
      bufferMs: Math.round(this.available / MS),
      minBufferMs: this.minLevel === Infinity ? 0 : Math.round(this.minLevel / MS),
      targetMs: Math.round(this.target / MS),
    });
    this.underruns = 0;
    this.minLevel = Infinity;
  }

  process(_inputs, outputs) {
    if (this.closed) return false;
    const out = outputs[0] && outputs[0][0];
    if (!out) return true;

    if (!this.playing && this.available >= this.target) this.playing = true;

    // Drain gently when the buffer holds far more than it needs.
    const step = this.available > this.target * 2 ? this.step * 1.02 : this.step;

    for (let i = 0; i < out.length; i += 1) {
      if (!this.playing) {
        // Ramp the last sample down instead of cutting to zero.
        if (this.fade > 0) {
          this.fade -= 1;
          out[i] = this.last * (this.fade / FADE);
        } else {
          out[i] = 0;
        }
        continue;
      }
      if (this.available < 2) {
        this.underrun();
        out[i] = this.last;
        continue;
      }
      const a = this.ring[this.read];
      const b = this.ring[(this.read + 1) % RING_SIZE];
      const v = a + (b - a) * this.frac;
      out[i] = v;
      this.last = v;
      this.frac += step;
      while (this.frac >= 1 && this.available >= 2) {
        this.frac -= 1;
        this.read = (this.read + 1) % RING_SIZE;
        this.available -= 1;
      }
    }

    if (this.playing) {
      this.smoothSamples += out.length;
      if (this.smoothSamples >= SHRINK_AFTER_S * sampleRate) {
        this.smoothSamples = 0;
        this.target = Math.max(MIN_TARGET, this.target - SHRINK_STEP);
      }
    }

    this.report(out.length);

    // Mirror to every output channel so stereo destinations play both sides.
    for (let c = 1; c < outputs[0].length; c += 1) outputs[0][c].set(out);
    return true;
  }
}

registerProcessor("playback-processor", PlaybackProcessor);
