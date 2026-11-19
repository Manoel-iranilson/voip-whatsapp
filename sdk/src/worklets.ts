import {
  CAPTURE_PROCESSOR_NAME,
  CAPTURE_FRAME_SIZE,
  PLAYBACK_PROCESSOR_NAME,
  SAMPLE_RATE,
} from "./constants.js";

/**
 * The worklets live here as source strings rather than as files under a public
 * directory.
 *
 * `audioWorklet.addModule` takes a URL, and the panel points it at
 * `/worklets/*.js` served by the Go binary. That only works while the page is
 * served from the same origin as the server. Inside somebody else's
 * application — a different origin, a bundler that hashes assets, a deploy
 * under a sub-path, a CDN — that path is a 404 and the call fails with no
 * audio and no useful error. A blob URL has no such dependency: nothing has to
 * be copied, served or configured.
 */

// Both processors run at the context's native rate (44.1/48 kHz) and convert
// to and from the server's 16 kHz themselves. Safari cannot reliably run a
// 16 kHz context against a microphone: it gives silent or distorted capture.
// Same code as the panel's client/public/worklets/*.js; keep them in step.

const captureSource = `
const DEFAULT_FRAME_SIZE = ${CAPTURE_FRAME_SIZE};
const DEFAULT_TARGET_RATE = ${SAMPLE_RATE};

// RBJ biquad low-pass, used as the anti-aliasing filter before decimation.
const lowpass = (cutoff, rate) => {
  const w0 = (2 * Math.PI * cutoff) / rate;
  const alpha = Math.sin(w0) / (2 * Math.SQRT1_2);
  const cos = Math.cos(w0);
  const a0 = 1 + alpha;
  return {
    b0: (1 - cos) / 2 / a0,
    b1: (1 - cos) / a0,
    b2: (1 - cos) / 2 / a0,
    a1: (-2 * cos) / a0,
    a2: (1 - alpha) / a0,
  };
};

class CaptureProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const opts = (options && options.processorOptions) || {};
    const size = opts.frameSize;
    const target = opts.targetRate;
    this.frameSize = Number.isInteger(size) && size > 0 ? size : DEFAULT_FRAME_SIZE;
    this.targetRate = target > 0 ? target : DEFAULT_TARGET_RATE;
    // Input samples consumed per output sample (3 at 48 kHz, 2.75625 at 44.1).
    this.step = sampleRate / this.targetRate;
    this.frame = new Float32Array(this.frameSize);
    this.pos = 0;
    // Position of the next output sample, in input samples, relative to prev.
    this.t = 0;
    this.prev = 0;
    // Two cascaded biquads give a steep enough roll-off below 8 kHz.
    this.filters =
      this.step > 1
        ? [lowpass(this.targetRate * 0.45, sampleRate), lowpass(this.targetRate * 0.45, sampleRate)].map((c) => ({
            ...c, x1: 0, x2: 0, y1: 0, y2: 0,
          }))
        : [];
    this.closed = false;
    this.port.onmessage = (e) => {
      if (e.data === "close") this.closed = true;
    };
  }

  filter(x) {
    let v = x;
    for (const f of this.filters) {
      const y = f.b0 * v + f.b1 * f.x1 + f.b2 * f.x2 - f.a1 * f.y1 - f.a2 * f.y2;
      f.x2 = f.x1;
      f.x1 = v;
      f.y2 = f.y1;
      f.y1 = y;
      v = y;
    }
    return v;
  }

  emit(sample) {
    this.frame[this.pos] = sample;
    this.pos += 1;
    if (this.pos === this.frameSize) {
      this.port.postMessage(this.frame.slice(0));
      this.pos = 0;
    }
  }

  process(inputs) {
    if (this.closed) return false;
    // Channel 0 only: the server expects mono.
    const channel = inputs[0] && inputs[0][0];
    if (!channel || !channel.length) return true;

    for (let i = 0; i < channel.length; i += 1) {
      const cur = this.filter(channel[i]);
      // Output samples falling between prev (t=0) and cur (t=1).
      while (this.t <= 1) {
        this.emit(this.prev + (cur - this.prev) * this.t);
        this.t += this.step;
      }
      this.t -= 1;
      this.prev = cur;
    }
    return true;
  }
}

registerProcessor(${JSON.stringify(CAPTURE_PROCESSOR_NAME)}, CaptureProcessor);
`;

// Frames arrive in bursts (network jitter, SCTP retransmits, a busy main
// thread on iOS). Playing each sample as soon as it lands turns every late
// frame into a gap: the choppy, cutting audio. So this is a small adaptive
// jitter buffer:
// - it holds back target samples before starting, and again after a gap;
// - every gap grows target a little (up to maxTarget), and it shrinks back
//   slowly while playback stays smooth;
// - when the buffer runs well above target (sender clock faster than ours) it
//   plays ~2% faster to drain it instead of letting latency pile up;
// - the ring itself is the hard latency ceiling (maxLatencyMs): when it is
//   full the oldest sample is dropped.
const playbackSource = `
const DEFAULT_SOURCE_RATE = ${SAMPLE_RATE};
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
    const capacity =
      Number.isInteger(opts.capacitySamples) && opts.capacitySamples > 0
        ? opts.capacitySamples
        : DEFAULT_SOURCE_RATE * 2;
    this.capacity = capacity;
    // The buffer target has to fit comfortably inside the latency ceiling.
    this.maxTarget = Math.max(MS * 20, Math.min(MAX_TARGET, Math.floor(capacity * 0.6)));
    this.minTarget = Math.min(MIN_TARGET, this.maxTarget);
    // Source samples advanced per output sample (1/3 at 48 kHz).
    this.step = source / sampleRate;
    this.ring = new Float32Array(capacity);
    this.read = 0;
    this.write = 0;
    this.available = 0;
    // Fractional position between ring[read] and ring[read + 1].
    this.frac = 0;
    this.target = Math.min(START_TARGET, this.maxTarget);
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
        this.write = (this.write + 1) % this.capacity;
        if (this.available < this.capacity) {
          this.available += 1;
        } else {
          this.read = (this.read + 1) % this.capacity;
        }
      }
    };
  }

  underrun() {
    this.playing = false;
    this.fade = FADE;
    this.smoothSamples = 0;
    this.target = Math.min(this.maxTarget, this.target + GROW_STEP);
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
      const b = this.ring[(this.read + 1) % this.capacity];
      const v = a + (b - a) * this.frac;
      out[i] = v;
      this.last = v;
      this.frac += step;
      while (this.frac >= 1 && this.available >= 2) {
        this.frac -= 1;
        this.read = (this.read + 1) % this.capacity;
        this.available -= 1;
      }
    }

    if (this.playing) {
      this.smoothSamples += out.length;
      if (this.smoothSamples >= SHRINK_AFTER_S * sampleRate) {
        this.smoothSamples = 0;
        this.target = Math.max(this.minTarget, this.target - SHRINK_STEP);
      }
    }

    this.report(out.length);

    // Mirror to every output channel so stereo destinations play both sides.
    for (let c = 1; c < outputs[0].length; c += 1) outputs[0][c].set(out);
    return true;
  }
}

registerProcessor(${JSON.stringify(PLAYBACK_PROCESSOR_NAME)}, PlaybackProcessor);
`;

const addModuleFromSource = async (ctx: AudioContext, source: string): Promise<void> => {
  const url = URL.createObjectURL(new Blob([source], { type: "application/javascript" }));
  try {
    await ctx.audioWorklet.addModule(url);
  } finally {
    URL.revokeObjectURL(url);
  }
};

/**
 * Registers both processors on the context. Safe to call once per context; a
 * second call throws from `registerProcessor`, so contexts are never shared
 * between calls.
 */
export const loadWorklets = async (ctx: AudioContext): Promise<void> => {
  await addModuleFromSource(ctx, captureSource);
  await addModuleFromSource(ctx, playbackSource);
};
