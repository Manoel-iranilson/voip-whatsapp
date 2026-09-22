// The mic delivers 128-sample blocks (~8 ms at 16 kHz). Posting each one puts
// ~125 messages per second on the data channel, per call, per operator.
// Batching into 20 ms frames cuts that to 50 with no audible added latency.
const DEFAULT_FRAME_SIZE = 320;

class CaptureProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const size = options && options.processorOptions && options.processorOptions.frameSize;
    this.frameSize = Number.isInteger(size) && size > 0 ? size : DEFAULT_FRAME_SIZE;
    this.frame = new Float32Array(this.frameSize);
    this.pos = 0;
  }

  process(inputs) {
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

registerProcessor("capture-processor", CaptureProcessor);
