import { getAudioContext } from "./audio-context";
import { isMine, useCalls } from "@/stores/calls";
import type { CallStatus, CallSummary } from "@/types/call";

/**
 * Call progress tones for this operator's own calls:
 * - ringback while an outbound call is ringing (Brazilian pattern: 425 Hz,
 *   1 s on, 4 s off);
 * - a short rising chime when the call is answered;
 * - three short descending beeps when it ends.
 *
 * Everything plays through the shared AudioContext, which is already unlocked
 * by the click that placed or accepted the call, so it works on Safari too.
 */

const VOLUME = 0.15;

type Tone = { freq: number; at: number; dur: number; gain?: number };

/** Schedules tones relative to now. Returns a stop function. */
const play = (tones: Tone[]): (() => void) => {
  let ctx: AudioContext;
  try {
    ctx = getAudioContext();
  } catch {
    return () => {};
  }
  const out = ctx.createGain();
  out.connect(ctx.destination);
  const now = ctx.currentTime + 0.01;
  for (const t of tones) {
    const osc = ctx.createOscillator();
    const env = ctx.createGain();
    osc.type = "sine";
    osc.frequency.value = t.freq;
    const start = now + t.at;
    const end = start + t.dur;
    const g = t.gain ?? VOLUME;
    // Short ramps so the tones do not click.
    env.gain.setValueAtTime(0, start);
    env.gain.linearRampToValueAtTime(g, start + 0.015);
    env.gain.setValueAtTime(g, end - 0.015);
    env.gain.linearRampToValueAtTime(0, end);
    osc.connect(env).connect(out);
    osc.start(start);
    osc.stop(end + 0.02);
  }
  return () => {
    try {
      out.disconnect();
    } catch {}
  };
};

const RINGBACK_PERIOD_MS = 5000;

const startRingback = (): (() => void) => {
  let stopTone = play([{ freq: 425, at: 0, dur: 1 }]);
  const t = setInterval(() => {
    stopTone = play([{ freq: 425, at: 0, dur: 1 }]);
  }, RINGBACK_PERIOD_MS);
  return () => {
    clearInterval(t);
    stopTone();
  };
};

const playAnswered = () =>
  play([
    { freq: 660, at: 0, dur: 0.12 },
    { freq: 880, at: 0.14, dur: 0.18 },
  ]);

const playEnded = () =>
  play([
    { freq: 480, at: 0, dur: 0.18 },
    { freq: 425, at: 0.25, dur: 0.18 },
    { freq: 380, at: 0.5, dur: 0.28 },
  ]);

const isRinging = (s: CallStatus) => s === "starting" || s === "ringing";

let installed = false;

export const installCallSounds = (): void => {
  if (installed) return;
  installed = true;

  // Last status seen for each call of ours, and the ringback playing for it.
  const seen = new Map<string, CallStatus>();
  const ringbacks = new Map<string, () => void>();

  const stopRingback = (id: string) => {
    ringbacks.get(id)?.();
    ringbacks.delete(id);
  };

  const mine = (c: CallSummary, own: Map<string, unknown>) => isMine(c) || own.has(c.callId);

  useCalls.subscribe((state) => {
    const present = new Set<string>();

    for (const c of state.calls) {
      if (!mine(c, state.ownConnections)) continue;
      present.add(c.callId);
      const prev = seen.get(c.callId);
      if (prev === c.status) continue;
      seen.set(c.callId, c.status);

      if (c.direction === "outbound" && isRinging(c.status)) {
        if (!ringbacks.has(c.callId)) ringbacks.set(c.callId, startRingback());
      } else if (c.status === "connected") {
        stopRingback(c.callId);
        // Not on first sight: a call already connected when the page loads.
        if (prev !== undefined) playAnswered();
      } else if (c.status === "ended") {
        stopRingback(c.callId);
        if (prev !== undefined) playEnded();
      }
    }

    // A call of ours that left the list has ended (call-ended removes it).
    // One still listed but no longer ours was taken over: no tone for that.
    const listed = new Set(state.calls.map((c) => c.callId));
    for (const [id, status] of seen) {
      if (present.has(id)) continue;
      seen.delete(id);
      stopRingback(id);
      if (status !== "ended" && !listed.has(id)) playEnded();
    }
  });
};
