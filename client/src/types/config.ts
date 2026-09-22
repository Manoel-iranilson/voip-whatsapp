/**
 * How the server handles call recording.
 *
 * - `off`: no recording directory is configured, so recording is unavailable.
 * - `optional`: a call is recorded only when the request asks for it.
 * - `always`: the server was started with -record-all and records everything.
 */
export type RecordingMode = "off" | "optional" | "always";

export type ServerConfig = {
  recording: RecordingMode;
};
