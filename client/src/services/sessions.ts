import { apiGet, apiPost, apiDelete, apiPostVoid } from "@/lib/api";
import type { SessionInfo } from "@/types/session";

export const listSessions = () =>
  apiGet<{ sessions: SessionInfo[] }>("/api/sessions").then((r) => r.sessions ?? []);

export const createSession = (name: string) =>
  apiPost<{ id: string }>("/api/sessions", { name });

export const deleteSession = (id: string) => apiDelete(`/api/sessions/${id}`);

export const logoutSession = (id: string) => apiPostVoid(`/api/sessions/${id}/logout`);

export const pairSession = (id: string) => apiPostVoid(`/api/sessions/${id}/pair`);
