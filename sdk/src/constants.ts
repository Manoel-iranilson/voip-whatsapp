/**
 * The audio contract with the Go server. These are not preferences — the server
 * decodes what arrives with these assumptions baked in, so changing any of them
 * produces audio that is silent, sped up or slowed down, with no error anywhere.
 */

/** The only sample rate the server understands, on both directions. */
export const SAMPLE_RATE = 16_000;

/**
 * Samples batched per data-channel message — 20 ms at 16 kHz.
 *
 * The mic delivers 128-sample blocks (~8 ms), which would be ~125 messages per
 * second per call. Batching cuts that to 50 with no audible added latency. The
 * server slices whatever arrives into its own 960-sample encode buffer, so this
 * is a bandwidth/latency tradeoff rather than a protocol requirement.
 */
export const CAPTURE_FRAME_SIZE = 320;

/** The server ignores every data channel whose label is not exactly this. */
export const PCM_CHANNEL_LABEL = "pcm";

/**
 * Worklet processor names. Namespaced because `registerProcessor` throws on a
 * duplicate name, and the host application may already have processors of its
 * own registered in the same realm.
 */
export const CAPTURE_PROCESSOR_NAME = "wacalls-capture";
export const PLAYBACK_PROCESSOR_NAME = "wacalls-playback";

/**
 * How long to wait for ICE gathering before sending the offer with whatever
 * candidates exist. The panel waits forever, which hangs the call for good if
 * gathering stalls; with no STUN configured this normally completes in a few
 * milliseconds anyway.
 */
export const ICE_GATHERING_TIMEOUT_MS = 2_000;

/**
 * Hard ceiling of the playback buffer, in milliseconds; past it the oldest
 * audio is dropped. Below it an adaptive jitter buffer holds 60–300 ms and
 * drains excess by playing ~2% faster. Matches the panel's 2 s ring; 300–500 ms
 * is the better setting for a live conversation.
 */
export const DEFAULT_MAX_LATENCY_MS = 2_000;
