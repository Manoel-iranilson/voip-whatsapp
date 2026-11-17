// Runs at the context's native rate (44.1/48 kHz) and emits 16 kHz frames.
// Safari cannot reliably run a 16 kHz context against a microphone, so the
// resampling happens here instead of in the AudioContext.
//
// The mic delivers 128-sample blocks. Posting each one puts ~125+ messages per
// second on the data channel, per call, per operator. Batching into 20 ms
// frames cuts that to 50 with no audible added latency.
const DEFAULT_FRAME_SIZE = 320;
const DEFAULT_TARGET_RATE = 16000;

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
    // Position of the next output sample, in input samples, relative to `prev`.
    this.t = 0;
    this.prev = 0;
    // Two cascaded biquads give a steep enough roll-off below 8 kHz.
    this.filters =
      this.step > 1
        ? [lowpass(this.targetRate * 0.45, sampleRate), lowpass(this.targetRate * 0.45, sampleRate)].map((c) => ({
            ...c,
            x1: 0,
            x2: 0,
            y1: 0,
            y2: 0,
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
    const channel = inputs[0] && inputs[0][0];
    if (!channel || !channel.length) return true;

    for (let i = 0; i < channel.length; i += 1) {
      const cur = this.filter(channel[i]);
      // Output samples falling between `prev` (t=0) and `cur` (t=1).
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

registerProcessor("capture-processor", CaptureProcessor);
