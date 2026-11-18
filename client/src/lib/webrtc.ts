import { apiPost } from "./api";
import { float32ToInt16LE, int16LEToFloat32 } from "./pcm";
import { getAudioContext, loadWorklets } from "./audio-context";
import {
  CAPTURE_FRAME_SIZE,
  CAPTURE_PROCESSOR_NAME,
  PCM_CHANNEL_LABEL,
  PLAYBACK_PROCESSOR_NAME,
  SAMPLE_RATE,
} from "../constants/audio";

export type OpenCall = {
  pc: RTCPeerConnection;
  micStream: MediaStream;
  /** Taps for the level meters, in the shared AudioContext. */
  micAnalyser: AnalyserNode;
  peerAnalyser: AnalyserNode;
  /** Once-a-second receive diagnostics; returns an unsubscribe function. */
  onStats: (fn: (s: CallStats) => void) => () => void;
  close: () => void;
};

/**
 * Where the peer's audio is getting cut. If `maxGapMs` is high, frames are
 * reaching the browser late (network, server or data channel). If gaps are low
 * but `underruns` still climbs, the browser's audio output is the problem.
 */
export type CallStats = {
  /** Frames received in the last second (50 expected for 20 ms frames). */
  frames: number;
  /** Audio received in the last second, in ms (about 1000 expected). */
  receivedMs: number;
  /** Longest wait between two frames in the last second. */
  maxGapMs: number;
  /** Times playback ran dry in the last second. */
  underruns: number;
  bufferMs: number;
  minBufferMs: number;
  targetMs: number;
  /** The context's real rate and output latency, which differ per device. */
  sampleRate: number;
  outputLatencyMs: number;
};

const AUDIO_CONSTRAINTS: MediaTrackConstraints = {
  echoCancellation: true,
  noiseSuppression: true,
  autoGainControl: true,
  channelCount: 1,
};

const getMic = async (micDeviceId: string | null): Promise<MediaStream> => {
  if (!micDeviceId) return navigator.mediaDevices.getUserMedia({ audio: AUDIO_CONSTRAINTS });
  try {
    return await navigator.mediaDevices.getUserMedia({
      audio: { ...AUDIO_CONSTRAINTS, deviceId: { exact: micDeviceId } },
    });
  } catch (e) {
    // Safari rotates device ids between sessions, so a remembered id can stop
    // matching anything. Fall back to the default mic rather than failing.
    if ((e as DOMException).name !== "OverconstrainedError" && (e as DOMException).name !== "NotFoundError") throw e;
    return navigator.mediaDevices.getUserMedia({ audio: AUDIO_CONSTRAINTS });
  }
};

export const openCall = async (
  sid: string,
  callId: string,
  micDeviceId: string | null,
): Promise<OpenCall> => {
  const ctx = getAudioContext();
  const micStream = await getMic(micDeviceId);
  const pc = new RTCPeerConnection({ iceServers: [] });

  const nodes: AudioNode[] = [];
  const stopAudio = () => {
    for (const n of nodes) {
      try {
        n.disconnect();
      } catch {}
      if (n instanceof AudioWorkletNode) n.port.postMessage("close");
    }
  };

  try {
    await loadWorklets(ctx);
    // Unlocked by the click that started this call; see audio-context.ts.
    if (ctx.state !== "running") void ctx.resume().catch(() => {});

    const dc = pc.createDataChannel(PCM_CHANNEL_LABEL, { ordered: true });
    dc.binaryType = "arraybuffer";

    const micSource = ctx.createMediaStreamSource(micStream);
    const captureNode = new AudioWorkletNode(ctx, CAPTURE_PROCESSOR_NAME, {
      numberOfOutputs: 1,
      outputChannelCount: [1],
      processorOptions: { frameSize: CAPTURE_FRAME_SIZE, targetRate: SAMPLE_RATE },
    });
    captureNode.port.onmessage = (e: MessageEvent<Float32Array>) => {
      if (dc.readyState === "open") dc.send(float32ToInt16LE(e.data));
    };
    const micAnalyser = ctx.createAnalyser();
    micAnalyser.fftSize = 1024;
    micSource.connect(captureNode);
    micSource.connect(micAnalyser);
    // A worklet only runs while it is pulled by the graph. Its output is
    // silence, so routing it to the speakers costs nothing.
    captureNode.connect(ctx.destination);

    const playbackNode = new AudioWorkletNode(ctx, PLAYBACK_PROCESSOR_NAME, {
      numberOfInputs: 0,
      numberOfOutputs: 1,
      outputChannelCount: [1],
      processorOptions: { sourceRate: SAMPLE_RATE },
    });
    const peerAnalyser = ctx.createAnalyser();
    peerAnalyser.fftSize = 1024;
    // Played straight from the context. Routing it through a
    // MediaStreamDestination into an <audio> element is silent on iOS Safari.
    playbackNode.connect(ctx.destination);
    playbackNode.connect(peerAnalyser);
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

    const statsListeners = new Set<(s: CallStats) => void>();
    playbackNode.port.onmessage = (e: MessageEvent<Omit<CallStats, "frames" | "receivedMs" | "maxGapMs" | "sampleRate" | "outputLatencyMs">>) => {
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
      for (const fn of statsListeners) fn(stats);
    };
    nodes.push(micSource, captureNode, micAnalyser, playbackNode, peerAnalyser);

    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    await new Promise<void>((resolve) => {
      if (pc.iceGatheringState === "complete") return resolve();
      pc.addEventListener("icegatheringstatechange", () => {
        if (pc.iceGatheringState === "complete") resolve();
      });
      // Safari can sit in "gathering" well after the host candidates are in;
      // with no ICE servers there is nothing more worth waiting for.
      setTimeout(resolve, 2000);
    });

    const { sdp_answer } = await apiPost<{ sdp_answer: string }>(
      `/api/sessions/${sid}/calls/${callId}/webrtc`,
      { sdp_offer: pc.localDescription!.sdp },
    );
    await pc.setRemoteDescription({ type: "answer", sdp: sdp_answer });

    return {
      pc,
      micStream,
      micAnalyser,
      peerAnalyser,
      onStats: (fn) => {
        statsListeners.add(fn);
        return () => statsListeners.delete(fn);
      },
      close: () => {
        statsListeners.clear();
        try {
          micStream.getTracks().forEach((t) => t.stop());
        } catch {}
        stopAudio();
        try {
          pc.close();
        } catch {}
      },
    };
  } catch (e) {
    micStream.getTracks().forEach((t) => t.stop());
    stopAudio();
    pc.close();
    throw e;
  }
};

/** Routes call audio to the chosen speaker, where the browser allows it. */
export const setOutputDevice = (deviceId: string | null): void => {
  const c = getAudioContext() as AudioContext & { setSinkId?: (id: string) => Promise<void> };
  if (typeof c.setSinkId !== "function") return;
  void c.setSinkId(deviceId ?? "").catch(() => {});
};
