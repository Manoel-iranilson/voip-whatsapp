import { CAPTURE_WORKLET_URL, PLAYBACK_WORKLET_URL } from "../constants/audio";

/**
 * One AudioContext for the whole app, at the device's native sample rate.
 *
 * Safari (iOS and macOS) is the reason this exists:
 * - It only lets a context start from inside a user gesture. A context created
 *   after `await`ing the API and getUserMedia stays "suspended" forever, so the
 *   call connects with no audio in either direction.
 * - Forcing `sampleRate: 16000` and feeding it a 44.1/48 kHz microphone gives
 *   silent or distorted capture. We keep the native rate and resample to and
 *   from 16 kHz inside the worklets instead.
 * - iOS caps how many contexts a page may hold, and each call used to open
 *   three (call, mic meter, peer meter) plus one for the ringtone.
 */

type AC = typeof AudioContext;

let ctx: AudioContext | null = null;
let worklets: Promise<void> | null = null;

const ctor = (): AC | undefined =>
  window.AudioContext ?? (window as unknown as { webkitAudioContext?: AC }).webkitAudioContext;

export const getAudioContext = (): AudioContext => {
  if (ctx && ctx.state !== "closed") return ctx;
  const C = ctor();
  if (!C) throw new Error("Web Audio is not supported in this browser.");
  ctx = new C({ latencyHint: "interactive" });
  worklets = null;
  return ctx;
};

/**
 * Starts (or restarts, after an iOS "interrupted" state) the shared context.
 * Must run synchronously inside a user gesture to have any effect on Safari.
 * Never awaited by callers: outside a gesture Safari leaves the promise pending.
 */
export const unlockAudio = (): void => {
  try {
    const c = getAudioContext();
    if (c.state !== "running") void c.resume().catch(() => {});
  } catch {}
};

export const loadWorklets = (c: AudioContext): Promise<void> => {
  if (!worklets) {
    worklets = Promise.all([
      c.audioWorklet.addModule(CAPTURE_WORKLET_URL),
      c.audioWorklet.addModule(PLAYBACK_WORKLET_URL),
    ]).then(() => undefined);
    worklets.catch(() => {
      worklets = null;
    });
  }
  return worklets;
};

let installed = false;

/** Unlocks the shared context on every user gesture, anywhere on the page. */
export const installAudioUnlock = (): void => {
  if (installed) return;
  installed = true;
  for (const ev of ["pointerup", "touchend", "click", "keydown"]) {
    window.addEventListener(ev, unlockAudio, { capture: true, passive: true });
  }
};
