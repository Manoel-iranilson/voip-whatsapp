import { getAuthToken } from "./auth-token";
import { getClientId } from "./client-id";
import { markAuthRequired } from "@/stores/auth";

export const baseHeaders = (): Record<string, string> => {
  const headers: Record<string, string> = {
    "X-Client-Id": getClientId(),
    "Content-Type": "application/json",
  };
  const token = getAuthToken();
  if (token) headers["X-Auth-Token"] = token;
  return headers;
};

const check401 = (r: Response): void => {
  if (r.status === 401) markAuthRequired();
};

export const apiGet = async <T>(path: string): Promise<T> => {
  const r = await fetch(path, { headers: baseHeaders() });
  check401(r);
  if (!r.ok) throw new Error(`${path} ${r.status}`);
  return r.json() as Promise<T>;
};

export const apiPost = async <T>(path: string, body: unknown): Promise<T> => {
  const r = await fetch(path, { method: "POST", headers: baseHeaders(), body: JSON.stringify(body) });
  check401(r);
  if (!r.ok) {
    const text = await r.text().catch(() => "");
    throw new Error(`${path} ${r.status} ${text}`);
  }
  return r.json() as Promise<T>;
};

export const apiDelete = async (path: string): Promise<void> => {
  const r = await fetch(path, { method: "DELETE", headers: baseHeaders() });
  check401(r);
  if (!r.ok) throw new Error(`${path} ${r.status}`);
};

/** POST with an empty body and no JSON reply, as the session actions use. */
export const apiPostVoid = async (path: string): Promise<void> => {
  const r = await fetch(path, { method: "POST", headers: baseHeaders(), body: "{}" });
  check401(r);
  if (!r.ok) throw new Error(`${path} ${r.status}`);
};
