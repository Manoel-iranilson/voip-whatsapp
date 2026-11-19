import type { WaCallsEvents } from "./events.js";
import type { CallRecord, CallStatus } from "./types.js";

export type CallSoundsOptions = {
  /** Brazilian ringback (425 Hz, 1 s on, 4 s off) while an outbound call rings. Default true. */
  ringback?: boolean;
  /** Ringtone while an incoming call is offered and nobody has taken it. Default true. */
  incoming?: boolean;
  /** A short rising chime when a call of this operator is answered. Default true. */
  answered?: boolean;
  /** Three descending beeps when a call of this operator ends. Default true. */
  ended?: boolean;
  /** 0–1. Default 0.15. */
  volume?: number;
};

type Tone = { freq: number; at: number; dur: number };

type AC = typeof AudioContext;

const GESTURES = ["pointerup", "touchend", "click", "keydown"] as const;
const RINGBACK_PERIOD_MS = 5000;
const INCOMING_PERIOD_MS = 3000;

const isRinging = (s: CallStatus) => s === "starting" || s === "ringing";

/**
 * Call progress tones, driven by the event stream.
 *
 * It has its own AudioContext rather than borrowing the call's: the call's
 * context exists only once audio is attached, and the ringback has to start
 * before that. Browsers only let a context start inside a user gesture, so the
 * context is created and resumed from gesture listeners — the click that places
 * the call is enough. A context created later, after awaiting the API, would
 * stay suspended and the tones would never be heard.
 *
 * A call counts as this operator's when the server says its owner is this
 * client, or when this client started, picked up or answered it (`isOwn`): the
 * owner can briefly read `null` while a call is being created.
 */
export class CallSounds {
  private readonly clientId: string;
  private readonly isOwn: (callId: string) => boolean;
  private readonly opts: Required<CallSoundsOptions>;

  private ctx: AudioContext | null = null;
  private readonly seen = new Map<string, CallStatus>();
  private readonly ringbacks = new Map<string, () => void>();
  private incoming: { callId: string; stop: () => void } | null = null;
  private readonly offs: Array<() => void> = [];
  private disposed = false;

  constructor(
    events: WaCallsEvents,
    clientId: string,
    isOwn: (callId: string) => boolean,
    options: CallSoundsOptions = {},
  ) {
    this.clientId = clientId;
    this.isOwn = isOwn;
    this.opts = {
      ringback: options.ringback ?? true,
      incoming: options.incoming ?? true,
      answered: options.answered ?? true,
      ended: options.ended ?? true,
      volume: Math.min(1, Math.max(0, options.volume ?? 0.15)),
    };

    if (typeof window !== "undefined") {
      for (const ev of GESTURES) {
        window.addEventListener(ev, this.unlock, { capture: true, passive: true });
      }
    }

    this.offs.push(
      events.on("call-list", (e) => this.onCallList(e.calls)),
      events.on("call-ended", (e) => this.onEnded(e.id, e.owner)),
      events.on("incoming", (e) => this.startIncoming(e.id)),
      events.on("incoming-claimed", (e) => this.stopIncoming(e.id)),
    );
  }

  /** Stops every tone and removes all listeners. */
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    for (const off of this.offs) off();
    this.offs.length = 0;
    if (typeof window !== "undefined") {
      for (const ev of GESTURES) window.removeEventListener(ev, this.unlock, { capture: true });
    }
    for (const stop of this.ringbacks.values()) stop();
    this.ringbacks.clear();
    this.incoming?.stop();
    this.incoming = null;
    this.seen.clear();
    const ctx = this.ctx;
    this.ctx = null;
    if (ctx && ctx.state !== "closed") void ctx.close().catch(() => {});
  }

  // ------------------------------------------------------------------ audio

  private readonly unlock = (): void => {
    try {
      const ctx = this.context();
      if (ctx && ctx.state !== "running") void ctx.resume().catch(() => {});
    } catch {
      // No Web Audio: the tones are a nicety, never a reason to fail.
    }
  };

  private context(): AudioContext | null {
    if (this.disposed || typeof window === "undefined") return null;
    if (this.ctx && this.ctx.state !== "closed") return this.ctx;
    const C: AC | undefined =
      window.AudioContext ?? (window as unknown as { webkitAudioContext?: AC }).webkitAudioContext;
    if (!C) return null;
    this.ctx = new C({ latencyHint: "interactive" });
    return this.ctx;
  }

  /** Schedules tones relative to now. Returns a function that silences them. */
  private play(tones: Tone[], gain = this.opts.volume): () => void {
    let ctx: AudioContext | null;
    try {
      ctx = this.context();
    } catch {
      ctx = null;
    }
    if (!ctx || gain <= 0) return () => {};
    // Outside a gesture this does nothing on Safari, but Chrome and Firefox
    // accept it once the page has had any interaction at all.
    if (ctx.state !== "running") void ctx.resume().catch(() => {});

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
      // Short ramps so the tones do not click.
      env.gain.setValueAtTime(0, start);
      env.gain.linearRampToValueAtTime(gain, start + 0.015);
      env.gain.setValueAtTime(gain, end - 0.015);
      env.gain.linearRampToValueAtTime(0, end);
      osc.connect(env).connect(out);
      osc.start(start);
      osc.stop(end + 0.02);
    }
    return () => {
      try {
        out.disconnect();
      } catch {
        // Already disconnected.
      }
    };
  }

  /** Plays `tones` now and every `periodMs` until the returned function runs. */
  private loop(tones: Tone[], periodMs: number): () => void {
    let stopTone = this.play(tones);
    const timer = setInterval(() => {
      stopTone = this.play(tones);
    }, periodMs);
    return () => {
      clearInterval(timer);
      stopTone();
    };
  }

  private playAnswered(): void {
    if (!this.opts.answered) return;
    this.play([
      { freq: 660, at: 0, dur: 0.12 },
      { freq: 880, at: 0.14, dur: 0.18 },
    ]);
  }

  private playEnded(): void {
    if (!this.opts.ended) return;
    this.play([
      { freq: 480, at: 0, dur: 0.18 },
      { freq: 425, at: 0.25, dur: 0.18 },
      { freq: 380, at: 0.5, dur: 0.28 },
    ]);
  }

  // ----------------------------------------------------------------- events

  private mine(callId: string, owner: string | null): boolean {
    return owner === this.clientId || this.isOwn(callId);
  }

  private stopRingback(callId: string): void {
    this.ringbacks.get(callId)?.();
    this.ringbacks.delete(callId);
  }

  private onCallList(calls: CallRecord[]): void {
    const listed = new Set<string>();

    for (const c of calls) {
      listed.add(c.callId);
      // Answered somewhere — by this operator or another — so stop ringing.
      if (this.incoming?.callId === c.callId && c.status !== "ringing" && c.status !== "starting") {
        this.stopIncoming(c.callId);
      }
      if (!this.mine(c.callId, c.owner)) {
        // Was ours and no longer is: taken over, which deserves no tone.
        if (this.seen.has(c.callId)) {
          this.seen.delete(c.callId);
          this.stopRingback(c.callId);
        }
        continue;
      }

      const prev = this.seen.get(c.callId);
      if (prev === c.status) continue;
      this.seen.set(c.callId, c.status);

      if (c.direction === "outbound" && isRinging(c.status)) {
        if (this.opts.ringback && !this.ringbacks.has(c.callId)) {
          this.ringbacks.set(
            c.callId,
            this.loop([{ freq: 425, at: 0, dur: 1 }], RINGBACK_PERIOD_MS),
          );
        }
      } else if (c.status === "connected") {
        this.stopRingback(c.callId);
        // Not on first sight: a call already up when the page loads.
        if (prev !== undefined) this.playAnswered();
      } else if (c.status === "ended") {
        this.onEnded(c.callId, c.owner);
      }
    }

    // A call that left the list has ended; `call-ended` normally gets there first.
    for (const id of [...this.seen.keys()]) {
      if (!listed.has(id)) this.onEnded(id, null);
    }
    if (this.incoming && !listed.has(this.incoming.callId)) this.stopIncoming(this.incoming.callId);
  }

  private onEnded(callId: string, owner: string | null): void {
    this.stopIncoming(callId);
    const wasOurs = this.seen.has(callId) || (owner !== null && owner === this.clientId);
    const prev = this.seen.get(callId);
    this.seen.delete(callId);
    this.stopRingback(callId);
    if (wasOurs && prev !== undefined && prev !== "ended") this.playEnded();
  }

  private startIncoming(callId: string): void {
    if (!this.opts.incoming || this.incoming?.callId === callId) return;
    this.incoming?.stop();
    // Two-tone ring, 1 s on, 2 s off.
    const stop = this.loop(
      [
        { freq: 440, at: 0, dur: 1 },
        { freq: 480, at: 0, dur: 1 },
      ],
      INCOMING_PERIOD_MS,
    );
    this.incoming = { callId, stop };
  }

  private stopIncoming(callId: string): void {
    if (this.incoming?.callId !== callId) return;
    this.incoming.stop();
    this.incoming = null;
  }
}
