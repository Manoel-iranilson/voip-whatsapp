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
import type { AttachAudioOptions, CallState } from "./types.js";
import { loadWorklets } from "./worklets.js";

type CallEventMap = {
  state: CallState;
  error: Error;
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

  /** Feed this to an `<audio>` element's `srcObject`. Available once attached. */
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
    const audio: MediaTrackConstraints = { ...(opts.audioConstraints ?? {}) };
    if (opts.micDeviceId) audio.deviceId = { exact: opts.micDeviceId };
    this.micStream = await navigator.mediaDevices.getUserMedia({
      audio: Object.keys(audio).length > 0 ? audio : true,
    });

    const pc = new RTCPeerConnection({ iceServers: [] });
    this.pc = pc;

    const dc = pc.createDataChannel(PCM_CHANNEL_LABEL, { ordered: true });
    dc.binaryType = "arraybuffer";
    this.dc = dc;

    // Everything rests on this: the browser resamples the 48 kHz microphone to
    // 16 kHz on the way into the graph, and there is no resampler anywhere else
    // in the stack. A browser that ignores the hint would send audio at three
    // times the rate the server expects, which sounds like chipmunks rather
    // than like a bug.
    const ctx = new AudioContext({ sampleRate: SAMPLE_RATE });
    this.ctx = ctx;
    if (Math.round(ctx.sampleRate) !== SAMPLE_RATE) {
      throw new Error(
        `this browser gave a ${ctx.sampleRate} Hz AudioContext; WaCalls needs ${SAMPLE_RATE} Hz`,
      );
    }

    await loadWorklets(ctx);
    await ctx.resume();

    this.buildCaptureGraph(ctx, dc);
    this.buildPlaybackGraph(ctx, dc, opts.maxLatencyMs ?? DEFAULT_MAX_LATENCY_MS);
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
      processorOptions: { frameSize: CAPTURE_FRAME_SIZE },
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

  private buildPlaybackGraph(ctx: AudioContext, dc: RTCDataChannel, maxLatencyMs: number): void {
    const capacitySamples = Math.max(
      CAPTURE_FRAME_SIZE,
      Math.round((maxLatencyMs * SAMPLE_RATE) / 1000),
    );
    const playbackNode = new AudioWorkletNode(ctx, PLAYBACK_PROCESSOR_NAME, {
      processorOptions: { capacitySamples },
    });

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

    dc.onmessage = (e: MessageEvent<ArrayBuffer>) => {
      playbackNode.port.postMessage(int16LEToFloat32(e.data));
    };

    this.playbackNode = playbackNode;
    this.streamDest = streamDest;
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

    const fresh = await navigator.mediaDevices.getUserMedia({
      audio: deviceId ? { deviceId: { exact: deviceId } } : true,
    });

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
    try {
      void this.ctx?.close();
    } catch {
      // Already closed.
    }
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
