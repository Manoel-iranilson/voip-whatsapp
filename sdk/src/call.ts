import {
  CAPTURE_FRAME_SIZE,
  CAPTURE_PROCESSOR_NAME,
  DEFAULT_MAX_LATENCY_MS,
  ICE_GATHERING_TIMEOUT_MS,
  PCM_CHANNEL_LABEL,
  PLAYBACK_PROCESSOR_NAME,
  SAMPLE_RATE,
} from "./constants.js";
import { float32ToInt16LE, int16LEToFloat32 } from "./pcm.js";
import { Transport, WaCallsError } from "./transport.js";
import type { AttachAudioOptions, CallState, CallStats } from "./types.js";
import { loadWorklets } from "./worklets.js";

type CallEventMap = {
  state: CallState;
  error: Error;
  /** Once a second while audio flows: where the peer's audio is getting cut. */
  stats: CallStats;
};

/**
 * What the panel asks for. The browser usually enables these by default, but
 * not everywhere, and an echo on a speakerphone call is the first complaint.
 */
const DEFAULT_AUDIO_CONSTRAINTS: MediaTrackConstraints = {
  echoCancellation: true,
  noiseSuppression: true,
  autoGainControl: true,
  channelCount: 1,
};

/**
 * Opens the microphone. A remembered deviceId can stop matching anything —
 * Safari rotates them between sessions — so that falls back to the default mic
 * instead of failing the call.
 */
const openMic = async (
  deviceId: string | null | undefined,
  extra: MediaTrackConstraints | undefined,
): Promise<MediaStream> => {
  const base: MediaTrackConstraints = { ...DEFAULT_AUDIO_CONSTRAINTS, ...(extra ?? {}) };
  if (!deviceId) return navigator.mediaDevices.getUserMedia({ audio: base });
  try {
    return await navigator.mediaDevices.getUserMedia({
      audio: { ...base, deviceId: { exact: deviceId } },
    });
  } catch (e) {
    const name = (e as DOMException).name;
    if (name !== "OverconstrainedError" && name !== "NotFoundError") throw e;
    return navigator.mediaDevices.getUserMedia({ audio: base });
  }
};

type Listener<K extends keyof CallEventMap> = (payload: CallEventMap[K]) => void;

/**
 * One operator's audio leg of one call.
 *
 * The object exists as soon as the call does; `attachAudio` is what puts a
 * microphone on it. That split matters because posting the SDP offer is also
 * what *claims* the call on the server — there is no separate claim request —
 * so a call created through the API stays ownerless until some browser attaches.
 */
export class WaCallsCall {
  readonly sessionId: string;
  readonly callId: string;

  private readonly transport: Transport;
  private pc: RTCPeerConnection | null = null;
  private dc: RTCDataChannel | null = null;
  private ctx: AudioContext | null = null;
  private micStream: MediaStream | null = null;
  private micSource: MediaStreamAudioSourceNode | null = null;
  private captureNode: AudioWorkletNode | null = null;
  private playbackNode: AudioWorkletNode | null = null;
  private streamDest: MediaStreamAudioDestinationNode | null = null;
  private pageHideHandler: (() => void) | null = null;
  private audioConstraints: MediaTrackConstraints | undefined;

  private _state: CallState = "idle";
  private _muted = false;
  private readonly listeners = new Map<keyof CallEventMap, Set<Listener<never>>>();

  constructor(transport: Transport, sessionId: string, callId: string) {
    this.transport = transport;
    this.sessionId = sessionId;
    this.callId = callId;
  }

  get state(): CallState {
    return this._state;
  }

  /**
   * Feed this to an `<audio>` element's `srcObject`. Available once attached,
   * and `null` with `output: "speakers"`, where the SDK plays the audio itself.
   */
  get remoteStream(): MediaStream | null {
    return this.streamDest?.stream ?? null;
  }

  /** The operator's own microphone, for a level meter. */
  get localStream(): MediaStream | null {
    return this.micStream;
  }

  get muted(): boolean {
    return this._muted;
  }

  on<K extends keyof CallEventMap>(event: K, handler: Listener<K>): () => void {
    let set = this.listeners.get(event);
    if (!set) {
      set = new Set();
      this.listeners.set(event, set);
    }
    set.add(handler as Listener<never>);
    return () => {
      set!.delete(handler as Listener<never>);
    };
  }

  private emit<K extends keyof CallEventMap>(event: K, payload: CallEventMap[K]): void {
    const set = this.listeners.get(event);
    if (!set) return;
    for (const handler of [...set]) {
      try {
        (handler as Listener<K>)(payload);
      } catch {
        // A broken listener must not take the call down with it.
      }
    }
  }

  private setState(next: CallState): void {
    if (this._state === next) return;
    this._state = next;
    this.emit("state", next);
  }

  /**
   * Opens the microphone, negotiates WebRTC with the server and starts carrying
   * audio. Resolves once the answer is applied — `state` becomes `connected`
   * slightly later, when the data channel opens.
   *
   * On failure the call is handed back (the server would otherwise keep a call
   * claimed by a browser that has no audio), except on 409, which means another
   * operator got there first and the call is legitimately theirs.
   */
  async attachAudio(opts: AttachAudioOptions = {}): Promise<void> {
    if (this._state !== "idle") throw new Error(`attachAudio: call is already ${this._state}`);
    this.setState("connecting");

    try {
      await this.negotiate(opts);
    } catch (err) {
      this.close();
      this.setState("failed");
      const conflict = err instanceof WaCallsError && err.status === 409;
      if (!conflict) {
        await this.transport
          .del(`/api/sessions/${this.sessionId}/calls/${this.callId}`)
          .catch(() => {});
      }
      this.emit("error", err instanceof Error ? err : new Error(String(err)));
      throw err;
    }
  }

  private async negotiate(opts: AttachAudioOptions): Promise<void> {
    this.audioConstraints = opts.audioConstraints;
    this.micStream = await openMic(opts.micDeviceId, opts.audioConstraints);

    const pc = new RTCPeerConnection({ iceServers: [] });
    this.pc = pc;

    const dc = pc.createDataChannel(PCM_CHANNEL_LABEL, { ordered: true });
    dc.binaryType = "arraybuffer";
    this.dc = dc;

    // The context runs at the device's native rate and the worklets resample
    // to and from 16 kHz. Forcing a 16 kHz context is what Safari cannot do: the
    // microphone comes out silent or distorted.
    const ctx = new AudioContext({ latencyHint: "interactive" });
    this.ctx = ctx;

    await loadWorklets(ctx);
    await ctx.resume();

    this.buildCaptureGraph(ctx, dc);
    this.buildPlaybackGraph(ctx, dc, opts.maxLatencyMs ?? DEFAULT_MAX_LATENCY_MS, opts.output ?? "element");
    this.watchConnection(pc, dc);

    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    await waitForIceGathering(pc, ICE_GATHERING_TIMEOUT_MS);

    const { sdp_answer } = await this.transport.post<{ sdp_answer: string }>(
      `/api/sessions/${this.sessionId}/calls/${this.callId}/webrtc`,
      { sdp_offer: pc.localDescription?.sdp },
    );
    await pc.setRemoteDescription({ type: "answer", sdp: sdp_answer });

    this.installPageHideHandler();
  }

  private buildCaptureGraph(ctx: AudioContext, dc: RTCDataChannel): void {
    const captureNode = new AudioWorkletNode(ctx, CAPTURE_PROCESSOR_NAME, {
      numberOfOutputs: 1,
      outputChannelCount: [1],
      processorOptions: { frameSize: CAPTURE_FRAME_SIZE, targetRate: SAMPLE_RATE },
    });
    captureNode.port.onmessage = (e: MessageEvent<Float32Array>) => {
      if (dc.readyState === "open") dc.send(float32ToInt16LE(e.data));
    };
    // Connecting to the destination keeps the node scheduled by the graph. It
    // adds no echo: the processor never writes to its outputs.
    captureNode.connect(ctx.destination);
    this.captureNode = captureNode;

    this.micSource = ctx.createMediaStreamSource(this.micStream!);
    this.micSource.connect(captureNode);
  }

  private buildPlaybackGraph(
    ctx: AudioContext,
    dc: RTCDataChannel,
    maxLatencyMs: number,
    output: "element" | "speakers",
  ): void {
    const capacitySamples = Math.max(
      CAPTURE_FRAME_SIZE,
      Math.round((maxLatencyMs * SAMPLE_RATE) / 1000),
    );
    const playbackNode = new AudioWorkletNode(ctx, PLAYBACK_PROCESSOR_NAME, {
      numberOfInputs: 0,
      numberOfOutputs: 1,
      outputChannelCount: [1],
      processorOptions: { capacitySamples, sourceRate: SAMPLE_RATE },
    });

    if (output === "speakers") {
      // Straight to the context's output. Routing through a
      // MediaStreamDestination into an <audio> element is silent on iOS Safari.
      playbackNode.connect(ctx.destination);
    } else {
      const streamDest = ctx.createMediaStreamDestination();
      // The processor writes one channel. Left at the default of two, the right
      // channel stays silent and some output paths play the call in one ear.
      try {
        streamDest.channelCount = 1;
        streamDest.channelCountMode = "explicit";
      } catch {
        // Not fatal — a browser that refuses this still plays, just in stereo.
      }
      playbackNode.connect(streamDest);
      this.streamDest = streamDest;
    }

    // Arrival bookkeeping for the `stats` event: frames that reach the browser
    // late are a network/server problem; underruns with no late frames are the
    // browser's audio output.
    let frames = 0;
    let receivedSamples = 0;
    let maxGap = 0;
    let lastArrival = 0;
    dc.onmessage = (e: MessageEvent<ArrayBuffer>) => {
      const now = performance.now();
      if (lastArrival) maxGap = Math.max(maxGap, now - lastArrival);
      lastArrival = now;
      frames += 1;
      const pcm = int16LEToFloat32(e.data);
      receivedSamples += pcm.length;
      // Transfer instead of copy: less work on the main thread per frame.
      playbackNode.port.postMessage(pcm, [pcm.buffer]);
    };

    type WorkletStats = Pick<CallStats, "underruns" | "bufferMs" | "minBufferMs" | "targetMs">;
    playbackNode.port.onmessage = (e: MessageEvent<WorkletStats>) => {
      // A gap still open counts too, or a total stall would read as zero.
      const openGap = lastArrival ? performance.now() - lastArrival : 0;
      const stats: CallStats = {
        ...e.data,
        frames,
        receivedMs: Math.round(receivedSamples / (SAMPLE_RATE / 1000)),
        maxGapMs: Math.round(Math.max(maxGap, openGap)),
        sampleRate: ctx.sampleRate,
        outputLatencyMs: Math.round(((ctx.outputLatency || 0) + (ctx.baseLatency || 0)) * 1000),
      };
      frames = 0;
      receivedSamples = 0;
      maxGap = 0;
      this.emit("stats", stats);
    };

    this.playbackNode = playbackNode;
  }

  /**
   * The panel has none of this: its only teardown path is an SSE `call-ended`
   * event, so when the stream is down the microphone stays open after the other
   * side hangs up. Owning the lifecycle here makes the SDK work with SSE or
   * without it.
   */
  private watchConnection(pc: RTCPeerConnection, dc: RTCDataChannel): void {
    dc.onopen = () => this.setState("connected");

    pc.onconnectionstatechange = () => {
      switch (pc.connectionState) {
        case "connected":
          if (dc.readyState === "open") this.setState("connected");
          break;
        case "failed":
          this.close();
          this.setState("failed");
          this.emit("error", new Error("the audio connection to the server failed"));
          break;
        case "closed":
          this.close();
          this.setState("ended");
          break;
        default:
          break;
      }
    };
  }

  private installPageHideHandler(): void {
    if (typeof window === "undefined") return;
    this.pageHideHandler = () => {
      // Fire and forget: the page is going away. `keepalive` is what lets the
      // request survive it.
      void this.transport
        .del(`/api/sessions/${this.sessionId}/calls/${this.callId}`, { keepalive: true })
        .catch(() => {});
      this.close();
    };
    window.addEventListener("pagehide", this.pageHideHandler);
  }

  /** Stops sending the operator's voice. The call stays up. */
  mute(muted: boolean): void {
    this._muted = muted;
    this.micStream?.getAudioTracks().forEach((t) => {
      t.enabled = !muted;
    });
  }

  /** Switches microphone mid-call without renegotiating. */
  async setMicDevice(deviceId: string | null): Promise<void> {
    if (!this.ctx || !this.captureNode) throw new Error("setMicDevice: audio is not attached");

    const fresh = await openMic(deviceId, this.audioConstraints);

    this.micSource?.disconnect();
    this.micStream?.getTracks().forEach((t) => t.stop());

    this.micStream = fresh;
    this.micSource = this.ctx.createMediaStreamSource(fresh);
    this.micSource.connect(this.captureNode);
    this.mute(this._muted);
  }

  getStats(): Promise<RTCStatsReport> | null {
    return this.pc?.getStats() ?? null;
  }

  /** Hangs up on the server, then tears down locally. */
  async end(): Promise<void> {
    try {
      await this.transport.del(`/api/sessions/${this.sessionId}/calls/${this.callId}`);
    } finally {
      this.close();
      this.setState("ended");
    }
  }

  /**
   * Releases microphone, audio context and peer connection without telling the
   * server. Use `end()` unless the call is already gone — this alone leaves it
   * up until the server's ICE timeout notices.
   */
  close(): void {
    if (this.pageHideHandler && typeof window !== "undefined") {
      window.removeEventListener("pagehide", this.pageHideHandler);
      this.pageHideHandler = null;
    }
    if (this.captureNode) this.captureNode.port.onmessage = null;
    if (this.playbackNode) this.playbackNode.port.onmessage = null;
    // Lets the processors return false and be collected instead of running on
    // in a graph nobody pulls.
    for (const node of [this.captureNode, this.playbackNode]) {
      try {
        node?.port.postMessage("close");
      } catch {
        // Port already closed.
      }
    }
    if (this.dc) this.dc.onmessage = null;
    if (this.pc) this.pc.onconnectionstatechange = null;

    // Disconnecting before closing the context keeps a half-torn-down graph
    // from being scheduled one more time.
    try {
      this.micSource?.disconnect();
      this.captureNode?.disconnect();
      this.playbackNode?.disconnect();
    } catch {
      // Already disconnected.
    }

    try {
      this.micStream?.getTracks().forEach((t) => t.stop());
    } catch {
      // Already stopped.
    }
    // close() returns a promise, so a try/catch never saw its rejection: a
    // second close() threw "Cannot close a closed AudioContext" as an uncaught
    // promise. Clearing the field makes a repeat close() a no-op.
    const ctx = this.ctx;
    this.ctx = null;
    if (ctx && ctx.state !== "closed") void ctx.close().catch(() => {});
    try {
      this.pc?.close();
    } catch {
      // Already closed.
    }

    this.micSource = null;
    this.captureNode = null;
    this.playbackNode = null;
    this.dc = null;
  }
}

/**
 * Vanilla ICE: the offer is only useful once gathering finishes, because the
 * server has no way to receive trickled candidates. The timeout exists because
 * waiting forever turns a stalled gather into a call that never connects and
 * never errors.
 */
const waitForIceGathering = (pc: RTCPeerConnection, timeoutMs: number): Promise<void> =>
  new Promise<void>((resolve) => {
    if (pc.iceGatheringState === "complete") {
      resolve();
      return;
    }
    const done = () => {
      clearTimeout(timer);
      pc.removeEventListener("icegatheringstatechange", onChange);
      resolve();
    };
    const onChange = () => {
      if (pc.iceGatheringState === "complete") done();
    };
    const timer = setTimeout(done, timeoutMs);
    pc.addEventListener("icegatheringstatechange", onChange);
  });
