import { apiGet } from "@/lib/api";
import { getAuthToken } from "@/lib/auth-token";
import type { HistoryRow } from "@/types/history";

export const fetchHistory = (sid: string) =>
  apiGet<{ rows: HistoryRow[] }>(`/api/sessions/${sid}/history?limit=50`).then((r) => r.rows ?? []);

/** Ended calls across every session, or one session when sid is given. */
export const fetchAllHistory = (sid: string | null, limit = 200) => {
  const params = new URLSearchParams({ limit: String(limit) });
  if (sid) params.set("session", sid);
  return apiGet<{ rows: HistoryRow[] }>(`/api/history?${params.toString()}`).then((r) => r.rows ?? []);
};

/** A plain link cannot send headers, so the token rides along as a query param. */
export const recordingUrl = (row: HistoryRow): string => {
  const token = getAuthToken();
  const qs = token ? `?token=${encodeURIComponent(token)}` : "";
  return `/api/sessions/${row.sessionId}/calls/${row.callId}/recording${qs}`;
};
