import type { Transport } from "./transport.js";
import type { ServerEvent, ServerEventType } from "./types.js";

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
    if (this.source) return;
    const url = this.transport.signedUrl("/api/events", { clientId: this.transport.clientId });
    // The credential rides in the query string because EventSource cannot send
    // headers. Anyone who can read the URL can drive the account — keep it out
    // of logs and analytics.
    const source = new EventSource(url);
    source.onmessage = (e: MessageEvent<string>) => {
      let parsed: ServerEvent;
      try {
        parsed = JSON.parse(e.data) as ServerEvent;
      } catch {
        return;
      }
      const set = this.listeners.get(parsed.type);
      if (!set) return;
      for (const handler of [...set]) {
        try {
          handler(parsed);
        } catch {
          // One bad handler must not stop the others.
        }
      }
    };
    this.source = source;
  }

  close(): void {
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
