export type HistoryRow = {
  sessionId: string;
  callId: string;
  peer: string;
  direction: string;
  startedAt: number;
  endedAt: number | null;
  endReason: string | null;
  recording?: string;
};
