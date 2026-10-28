import { useQuery } from "@tanstack/react-query";
import { fetchAllHistory, fetchHistory } from "@/services/history";

export const useHistory = (sid: string | null, enabled: boolean) =>
  useQuery({
    queryKey: ["history", sid],
    queryFn: () => fetchHistory(sid as string),
    enabled: enabled && !!sid,
  });

// Shares the "history" key prefix so the call-ended invalidation refreshes it too.
export const useAllHistory = (sid: string | null) =>
  useQuery({
    queryKey: ["history", "all", sid],
    queryFn: () => fetchAllHistory(sid),
  });
