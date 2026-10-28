import { create } from "zustand";
import { clearAuthToken, getAuthToken, setAuthToken } from "@/lib/auth-token";
import { getClientId } from "@/lib/client-id";

type AuthStatus = "checking" | "ok" | "required";

export const useAuth = create<{ status: AuthStatus }>(() => ({ status: "checking" }));

const probe = async (token: string): Promise<boolean> => {
  const headers: Record<string, string> = { "X-Client-Id": getClientId() };
  if (token) headers["X-Auth-Token"] = token;
  const r = await fetch("/api/auth/check", { headers });
  if (r.status === 401) return false;
  if (!r.ok) throw new Error(`/api/auth/check ${r.status}`);
  return true;
};

/** Checks the stored token (or no token, for a server started without one). */
export const checkAuth = async (): Promise<void> => {
  try {
    useAuth.setState({ status: (await probe(getAuthToken())) ? "ok" : "required" });
  } catch {
    // The server is unreachable or too old for /api/auth/check; let the panel load
    // and surface errors on its own requests instead of locking the user out.
    useAuth.setState({ status: "ok" });
  }
};

/** Validates a token typed on the login screen and keeps it when accepted. */
export const login = async (token: string): Promise<boolean> => {
  if (!(await probe(token))) return false;
  setAuthToken(token);
  // Reload so the event stream reopens with the new token; one opened before a
  // mid-session 401 would otherwise keep the stale one.
  window.location.reload();
  return true;
};

/** Any 401 from the API means the stored token no longer works. */
export const markAuthRequired = (): void => {
  clearAuthToken();
  useAuth.setState({ status: "required" });
};

export const logout = (): void => {
  clearAuthToken();
  // A reload drops the open event stream and every cached piece of state.
  window.location.reload();
};
