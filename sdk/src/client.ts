import { WaCallsCall } from "./call.js";
import { WaCallsEvents } from "./events.js";
import { Transport } from "./transport.js";
import type {
  AttachAudioOptions,
  CallRecord,
  RecordingMode,
  SessionInfo,
  StartCallOptions,
  WaCallsClientOptions,
} from "./types.js";

type ClaimOptions = AttachAudioOptions & { sessionId: string; callId: string };

/** Entry point: one per operator session in the browser. */
export class WaCallsClient {
  readonly events: WaCallsEvents;
  private readonly transport: Transport;

  constructor(options: WaCallsClientOptions) {
    this.transport = new Transport(options);
    this.events = new WaCallsEvents(this.transport);
  }

  /** The identity the server ties call ownership to. */
  get clientId(): string {
    return this.transport.clientId;
  }

  // ---------------------------------------------------------------- sessions

  async listSessions(): Promise<SessionInfo[]> {
    const { sessions } = await this.transport.get<{ sessions: SessionInfo[] }>("/api/sessions");
    return sessions;
  }

  /**
   * The QR code for the new account does not come back here — it arrives on the
   * event stream as `session-qr`, so connect `events` before calling this if
   * you intend to display it.
   */
  async createSession(name: string): Promise<string> {
    const { id } = await this.transport.post<{ id: string }>("/api/sessions", { name });
    return id;
  }

  /**
   * Requests a fresh QR for an unpaired session. Like `createSession`, the QR
   * arrives on the event stream as `session-qr`, not in this response. Fails
   * with 400 when the session is already paired.
   */
  async pairSession(sessionId: string): Promise<void> {
    await this.transport.post(`/api/sessions/${sessionId}/pair`, {});
  }

  /** Disconnects the WhatsApp account but keeps the session so it can be paired again. */
  async logoutSession(sessionId: string): Promise<void> {
    await this.transport.post(`/api/sessions/${sessionId}/logout`, {});
  }

  /**
   * Disconnects and deletes the session. Its recordings and history rows stay
   * on the server and become unreachable through the API.
   */
  async deleteSession(sessionId: string): Promise<void> {
    await this.transport.del(`/api/sessions/${sessionId}`);
  }

  /** `off`, `optional` or `always` — whether `record: true` will do anything. */
  async recordingMode(): Promise<RecordingMode> {
    const { recording } = await this.transport.get<{ recording: RecordingMode }>("/api/config");
    return recording;
  }

  // ------------------------------------------------------------------- calls

  /**
   * Dials out. The returned call has no audio yet: call `attachAudio` on it, or
   * use `startAndAttach` to do both.
   */
  async startCall(opts: StartCallOptions): Promise<WaCallsCall> {
    const { call } = await this.transport.post<{ call: { callId: string } }>(
      `/api/sessions/${opts.sessionId}/calls`,
      {
        phone: opts.phone,
        record: opts.record ?? false,
        duration_ms: opts.durationMs ?? 0,
        operator: opts.operator ?? "",
      },
    );
    return new WaCallsCall(this.transport, opts.sessionId, call.callId);
  }

  /** Dials out and puts this operator's microphone on the call in one step. */
  async startAndAttach(opts: StartCallOptions & AttachAudioOptions): Promise<WaCallsCall> {
    const call = await this.startCall(opts);
    await call.attachAudio(opts);
    return call;
  }

  /**
   * Takes over a call nobody owns — typically one your backend started through
   * the API. Attaching the audio is itself the claim, so a second operator
   * trying the same call gets a 409.
   */
  async pickUp(opts: ClaimOptions): Promise<WaCallsCall> {
    const call = new WaCallsCall(this.transport, opts.sessionId, opts.callId);
    await call.attachAudio(opts);
    return call;
  }

  /** Answers an incoming call: accepts it on WhatsApp, then attaches audio. */
  async answer(opts: ClaimOptions): Promise<WaCallsCall> {
    await this.transport.post(
      `/api/sessions/${opts.sessionId}/calls/${opts.callId}/accept`,
      {},
    );
    const call = new WaCallsCall(this.transport, opts.sessionId, opts.callId);
    await call.attachAudio(opts);
    return call;
  }

  async reject(sessionId: string, callId: string): Promise<void> {
    await this.transport.post(`/api/sessions/${sessionId}/calls/${callId}/reject`, {});
  }

  /** Hangs up a call this client is not holding audio for. */
  async endCall(sessionId: string, callId: string): Promise<void> {
    await this.transport.del(`/api/sessions/${sessionId}/calls/${callId}`);
  }

  // --------------------------------------------------------------- history

  /** The 50 most recent calls of the session, newest first. */
  async history(sessionId: string): Promise<CallRecord[]> {
    const { rows } = await this.transport.get<{ rows: CallRecord[] }>(
      `/api/sessions/${sessionId}/history`,
    );
    return rows;
  }

  /**
   * A URL that plays or downloads the recording, credential included so it can
   * go straight into `<audio src>`. Only meaningful once the call has ended and
   * its history row carries a `recording` value.
   */
  recordingUrl(sessionId: string, callId: string): string {
    return this.transport.signedUrl(`/api/sessions/${sessionId}/calls/${callId}/recording`);
  }
}
