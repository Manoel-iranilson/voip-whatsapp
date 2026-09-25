import type { WaCallsClientOptions } from "./types.js";

/** An HTTP failure from the server, with the status kept so callers can branch on it. */
export class WaCallsError extends Error {
  readonly status: number;
  readonly body: string;

  constructor(method: string, path: string, status: number, body: string) {
    super(`${method} ${path} → ${status}${body ? ` ${body}` : ""}`);
    this.name = "WaCallsError";
    this.status = status;
    this.body = body;
  }
}

const DEFAULT_STORAGE_KEY = "wacalls.clientId";

const randomId = (): string => {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `c-${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
};

/**
 * A clientId that survives reloads. Ownership of a call is tied to it, so an id
 * that changes on every load means the operator loses the call they are on.
 * localStorage can throw (private mode, blocked storage), and a per-session id
 * is better than a crash.
 */
const persistedClientId = (storageKey: string): string => {
  try {
    const existing = localStorage.getItem(storageKey);
    if (existing) return existing;
    const fresh = randomId();
    localStorage.setItem(storageKey, fresh);
    return fresh;
  } catch {
    return randomId();
  }
};

export class Transport {
  readonly baseUrl: string;
  readonly clientId: string;
  private readonly token: string;
  private readonly fetchImpl: typeof fetch;

  constructor(opts: WaCallsClientOptions) {
    if (!opts.baseUrl) throw new Error("baseUrl is required");
    if (!opts.token) throw new Error("token is required");
    this.baseUrl = opts.baseUrl.replace(/\/+$/, "");
    this.token = opts.token;
    this.clientId = opts.clientId ?? persistedClientId(opts.storageKey ?? DEFAULT_STORAGE_KEY);
    this.fetchImpl = opts.fetchImpl ?? globalThis.fetch.bind(globalThis);
  }

  url(path: string): string {
    return `${this.baseUrl}${path}`;
  }

  /**
   * `X-Auth-Token` and `X-Client-Id` rather than `X-API-Key`: the server's CORS
   * preflight allows these two, `Authorization` and `Content-Type`, and nothing
   * else. A cross-origin request carrying `X-API-Key` is rejected by the
   * browser before it reaches the server.
   */
  headers(): Record<string, string> {
    return {
      "Content-Type": "application/json",
      "X-Client-Id": this.clientId,
      "X-Auth-Token": this.token,
    };
  }

  /** Signed URL for use where headers cannot travel: `<audio src>`, EventSource. */
  signedUrl(path: string, params: Record<string, string> = {}): string {
    const url = new URL(this.url(path));
    url.searchParams.set("token", this.token);
    for (const [k, v] of Object.entries(params)) url.searchParams.set(k, v);
    return url.toString();
  }

  async get<T>(path: string): Promise<T> {
    const r = await this.fetchImpl(this.url(path), { headers: this.headers() });
    if (!r.ok) throw new WaCallsError("GET", path, r.status, await r.text().catch(() => ""));
    return (await r.json()) as T;
  }

  async post<T>(path: string, body: unknown): Promise<T> {
    const r = await this.fetchImpl(this.url(path), {
      method: "POST",
      headers: this.headers(),
      body: JSON.stringify(body ?? {}),
    });
    if (!r.ok) throw new WaCallsError("POST", path, r.status, await r.text().catch(() => ""));
    return (await r.json()) as T;
  }

  /**
   * `keepalive` lets the request outlive the page, which is what makes hanging
   * up on `pagehide` actually reach the server. Without it, closing the tab
   * leaves the call up until the server's ICE timeout notices — several seconds
   * of a live WhatsApp call with nobody on it.
   */
  async del(path: string, opts: { keepalive?: boolean } = {}): Promise<void> {
    const r = await this.fetchImpl(this.url(path), {
      method: "DELETE",
      headers: this.headers(),
      keepalive: opts.keepalive,
    });
    if (!r.ok && r.status !== 404) {
      throw new WaCallsError("DELETE", path, r.status, await r.text().catch(() => ""));
    }
  }
}
