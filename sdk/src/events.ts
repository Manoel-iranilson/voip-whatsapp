import type { Transport } from "./transport.js";
import type { CallStatus, ServerEvent, ServerEventType } from "./types.js";

const RETRY_MIN_MS = 1000;
const RETRY_MAX_MS = 15000;

type Handler<T extends ServerEventType> = (event: Extract<ServerEvent, { type: T }>) => void;

/**
 * The server's event stream.
 *
 * Audio does not need it: a call placed through `startCall` works over plain
 * HTTP. It is required for anything the server initiates — incoming calls and
 * pairing QR codes arrive here and nowhere else, with no polling endpoint to
 * fall back on.
 *
 * An instance, not a module singleton, so a single-page app can tear it down
 * and rebuild it on logout or when the operator changes.
 */
export class WaCallsEvents {
  private readonly transport: Transport;
  private source: EventSource | null = null;
  private readonly listeners = new Map<ServerEventType, Set<(event: ServerEvent) => void>>();
  /** True between `connect()` and `close()`: the caller wants a live stream. */
  private wanted = false;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private retryDelayMs = RETRY_MIN_MS;
  /** Set when a stream opens; the next `call-list` is then the server's snapshot. */
  private awaitingSnapshot = false;
  /** Last status seen per live call, to tell what a snapshot changed. */
  private readonly known = new Map<string, { sessionId: string; owner: string | null; status: CallStatus }>();

  constructor(transport: Transport) {
    this.transport = transport;
  }

  get connected(): boolean {
    return this.source !== null && this.source.readyState !== EventSource.CLOSED;
  }

  /**
   * Idempotent. The browser reconnects on its own after a drop, and dropping
   * the stream never ends a call that is already up — the server only
   * unsubscribes the listener.
   */
  connect(): void {
    this.wanted = true;
    if (this.source) return;
    this.open();
  }

  private open(): void {
    const url = this.transport.signedUrl("/api/events", { clientId: this.transport.clientId });
    // The credential rides in the query string because EventSource cannot send
    // headers. Anyone who can read the URL can drive the account — keep it out
    // of logs and analytics.
    const source = new EventSource(url);
    source.onopen = () => {
      this.retryDelayMs = RETRY_MIN_MS;
      this.awaitingSnapshot = true;
    };
    source.onmessage = (e: MessageEvent<string>) => {
      let parsed: ServerEvent;
      try {
        parsed = JSON.parse(e.data) as ServerEvent;
      } catch {
        return;
      }
      this.track(parsed);
      this.dispatch(parsed);
    };
    // The browser retries a dropped connection by itself, but gives up for good
    // when the server answers with an HTTP error — which is exactly what a
    // reverse proxy returns while the server restarts for a deploy. A stream
    // left closed there never delivers `call-status` again: calls still carry
    // audio, yet stay "ringing" on screen until the page is reloaded.
    source.onerror = () => {
      if (source !== this.source || source.readyState !== EventSource.CLOSED) return;
      this.source = null;
      this.scheduleReconnect();
    };
    this.source = source;
  }

  private scheduleReconnect(): void {
    if (!this.wanted || this.retryTimer !== null) return;
    const delay = this.retryDelayMs;
    this.retryDelayMs = Math.min(this.retryDelayMs * 2, RETRY_MAX_MS);
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      if (this.wanted && !this.source) this.open();
    }, delay);
  }

  private dispatch(event: ServerEvent): void {
    const set = this.listeners.get(event.type);
    if (!set) return;
    for (const handler of [...set]) {
      try {
        handler(event);
      } catch {
        // One bad handler must not stop the others.
      }
    }
  }

  /**
   * Keeps `known` current and, on the snapshot that opens a stream, replays
   * what was missed while it was down. The server's snapshot is a bare
   * `call-list`; code that follows a call through `call-status` and
   * `call-ended` would otherwise never learn that it was answered or hung up
   * during the gap.
   */
  private track(event: ServerEvent): void {
    if (event.type === "call-status") {
      if (event.status === "ended") this.known.delete(event.id);
      else this.known.set(event.id, { sessionId: event.sessionId, owner: event.owner, status: event.status });
      return;
    }
    if (event.type === "call-ended") {
      this.known.delete(event.id);
      return;
    }
    if (event.type !== "call-list" || !this.awaitingSnapshot) return;
    this.awaitingSnapshot = false;

    const live = new Set<string>();
    for (const c of event.calls) {
      live.add(c.callId);
      const before = this.known.get(c.callId);
      this.known.set(c.callId, { sessionId: c.sessionId, owner: c.owner, status: c.status });
      if (before && before.status === c.status) continue;
      this.dispatch({
        type: "call-status",
        sessionId: c.sessionId,
        id: c.callId,
        owner: c.owner,
        status: c.status,
        peer: c.peer,
        startedAt: c.startedAt,
      });
    }
    for (const [id, c] of [...this.known]) {
      if (live.has(id)) continue;
      this.known.delete(id);
      this.dispatch({
        type: "call-ended",
        sessionId: c.sessionId,
        id,
        owner: c.owner,
        reason: "unknown",
        endedAt: Date.now(),
      });
    }
  }

  close(): void {
    this.wanted = false;
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    this.retryDelayMs = RETRY_MIN_MS;
    this.source?.close();
    this.source = null;
  }

  on<T extends ServerEventType>(type: T, handler: Handler<T>): () => void {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    const erased = handler as (event: ServerEvent) => void;
    set.add(erased);
    return () => {
      set!.delete(erased);
    };
  }
}
