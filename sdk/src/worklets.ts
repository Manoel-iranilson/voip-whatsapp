import {
  CAPTURE_PROCESSOR_NAME,
  CAPTURE_FRAME_SIZE,
  PLAYBACK_PROCESSOR_NAME,
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

const captureSource = `
const DEFAULT_FRAME_SIZE = ${CAPTURE_FRAME_SIZE};

class CaptureProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const size = options && options.processorOptions && options.processorOptions.frameSize;
    this.frameSize = Number.isInteger(size) && size > 0 ? size : DEFAULT_FRAME_SIZE;
    this.frame = new Float32Array(this.frameSize);
    this.pos = 0;
  }

  process(inputs) {
    // Channel 0 only. A stereo mic loses its right channel rather than being
    // downmixed, which matches what the server expects: mono.
    const channel = inputs[0] && inputs[0][0];
    if (!channel || !channel.length) return true;

    let offset = 0;
    while (offset < channel.length) {
      const n = Math.min(channel.length - offset, this.frameSize - this.pos);
      this.frame.set(channel.subarray(offset, offset + n), this.pos);
      this.pos += n;
      offset += n;
      if (this.pos === this.frameSize) {
        this.port.postMessage(this.frame.slice(0));
        this.pos = 0;
      }
    }
    return true;
  }
}

registerProcessor(${JSON.stringify(CAPTURE_PROCESSOR_NAME)}, CaptureProcessor);
`;

const playbackSource = `
class PlaybackProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const opts = (options && options.processorOptions) || {};
    const capacity =
      Number.isInteger(opts.capacitySamples) && opts.capacitySamples > 0
        ? opts.capacitySamples
        : 32000;
    this.ring = new Float32Array(capacity);
    this.capacity = capacity;
    this.read = 0;
    this.write = 0;
    this.available = 0;

    this.port.onmessage = (e) => {
      const data = e.data;
      for (let i = 0; i < data.length; i += 1) {
        this.ring[this.write] = data[i];
        this.write = (this.write + 1) % this.capacity;
        if (this.available < this.capacity) {
          this.available += 1;
        } else {
          // Full: drop the oldest sample. This is what caps latency — without
          // it a slow drain would keep growing the delay forever.
          this.read = (this.read + 1) % this.capacity;
        }
      }
    };
  }

  process(_inputs, outputs) {
    const out = outputs[0][0];
    if (!out) return true;
    for (let i = 0; i < out.length; i += 1) {
      if (this.available > 0) {
        out[i] = this.ring[this.read];
        this.read = (this.read + 1) % this.capacity;
        this.available -= 1;
      } else {
        // Underrun. Silence, not a repeat of the last sample: a gap is less
        // unpleasant than a buzz.
        out[i] = 0;
      }
    }
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
