import type { SessionState } from "@/types/session";

export const statusLabel: Record<SessionState, string> = {
  open: "Conectado",
  qr: "Aguardando QR",
  connecting: "Conectando…",
  logged_out: "Desconectado",
};

export const statusVariant: Record<SessionState, "success" | "secondary" | "muted" | "destructive"> = {
  open: "success",
  qr: "secondary",
  connecting: "muted",
  logged_out: "destructive",
};

/** The phone number part of a WhatsApp JID, e.g. "5511999999999". */
export const jidNumber = (jid: string): string => jid.split("@")[0].split(":")[0];
