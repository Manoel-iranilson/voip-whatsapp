/** Call lifecycle as the server reports it over SSE and webhooks. */
export type CallStatus = "starting" | "ringing" | "connected" | "ended";

/** Whether the server was started with recording available. */
export type RecordingMode = "off" | "optional" | "always";

/** A row of `GET /api/sessions/{sid}/history`, and of the SSE `call-list`. */
export type CallRecord = {
  sessionId: string;
  callId: string;
  /** `null` when no browser has claimed the call — typically one the API started. */
  owner: string | null;
  direction: "inbound" | "outbound";
  peer: string;
  /** Unix milliseconds. */
  startedAt: number;
  status: CallStatus;
  endedAt?: number;
  endReason?: string;
  /**
   * Present and non-empty exactly when a recording exists. It is the file stem,
   * not a path — the server never exposes where the file lives. Absent means no
   * recording, so test for presence rather than for a truthy boolean.
   */
  recording?: string;
};

export type SessionInfo = {
  id: string;
  name: string;
  jid: string;
  state: string;
  paired: boolean;
};

/** Local state of the browser's audio leg. Not the same as the call's status. */
export type CallState = "idle" | "connecting" | "connected" | "ended" | "failed";

export type WaCallsClientOptions = {
  /** Origin of the WaCalls server, no trailing path. */
  baseUrl: string;
  /**
   * The operator token (`WACALLS_TOKEN`). An integration key works for control
   * endpoints but produces unowned calls, so use the operator token in a
   * browser that is going to carry audio.
   */
  token: string;
  /**
   * Stable identity of this operator. The server uses it to decide who owns a
   * call, so it must survive reloads — otherwise the operator loses the calls
   * they are on. Defaults to a random id persisted in localStorage.
   */
  clientId?: string;
  /** Key used for the generated clientId. Set it when two products share an origin. */
  storageKey?: string;
  /** Injectable for tests or for a fetch wrapper with your own retry policy. */
  fetchImpl?: typeof fetch;
};

export type AttachAudioOptions = {
  /** `deviceId` from `navigator.mediaDevices.enumerateDevices()`. */
  micDeviceId?: string | null;
  /**
   * Ceiling for the playback buffer. Defaults to 2000 ms, matching the WaCalls
   * panel. Lower it (300–500) for a conversation that stays responsive.
   */
  maxLatencyMs?: number;
  /**
   * Constraints merged into the `getUserMedia` audio request — use it to turn
   * off echo cancellation or noise suppression, which the browser enables by
   * default.
   */
  audioConstraints?: MediaTrackConstraints;
};

export type StartCallOptions = {
  sessionId: string;
  /** Digits with country code; anything else is stripped by the server. */
  phone: string;
  record?: boolean;
  /** Hangs up automatically after this long. The only automatic duration cap. */
  durationMs?: number;
  /** Assign the call to another operator's clientId instead of leaving it open. */
  operator?: string;
};

/** Events pushed over `GET /api/events`. */
export type ServerEvent =
  | { type: "session-list"; sessions: SessionInfo[] }
  | { type: "session-qr"; sessionId: string; qr: string }
  | { type: "auth-state"; sessionId: string; paired: boolean; state: string; qr?: string }
  | { type: "call-list"; calls: CallRecord[] }
  | {
      type: "call-status";
      sessionId: string;
      id: string;
      owner: string | null;
      status: CallStatus;
      peer: string;
      startedAt: number;
    }
  | {
      type: "call-ended";
      sessionId: string;
      id: string;
      owner: string | null;
      reason: string;
      endedAt: number;
    }
  | { type: "incoming"; sessionId: string; id: string; peer: string; offeredAt: number }
  | { type: "incoming-claimed"; sessionId: string; id: string; owner: string };

export type ServerEventType = ServerEvent["type"];
