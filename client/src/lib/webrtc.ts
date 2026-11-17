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
  close: () => void;
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
    dc.onmessage = (e: MessageEvent<ArrayBuffer>) => {
      playbackNode.port.postMessage(int16LEToFloat32(e.data));
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
      close: () => {
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
