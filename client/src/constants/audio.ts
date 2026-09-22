export const SAMPLE_RATE = 16000;
/** Samples the capture worklet batches before sending — 20 ms at 16 kHz. */
export const CAPTURE_FRAME_SIZE = 320;
export const PCM_CHANNEL_LABEL = "pcm";
export const CAPTURE_WORKLET_URL = "/worklets/capture-processor.js";
export const PLAYBACK_WORKLET_URL = "/worklets/playback-processor.js";
export const CAPTURE_PROCESSOR_NAME = "capture-processor";
export const PLAYBACK_PROCESSOR_NAME = "playback-processor";
