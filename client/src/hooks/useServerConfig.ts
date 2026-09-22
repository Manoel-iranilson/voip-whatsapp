import { useQuery } from "@tanstack/react-query";
import { fetchConfig } from "@/services/config";
import type { RecordingMode } from "@/types/config";

// The server's settings only change when it restarts, so this never goes stale
// within a session.
export const useServerConfig = () =>
  useQuery({
    queryKey: ["config"],
    queryFn: fetchConfig,
    staleTime: Infinity,
  });

/**
 * The recording mode, falling back to `optional` while the request is in flight
 * or if it fails. That fallback is the long-standing behaviour of the dialer, so
 * the button does not flicker into a different shape on load.
 */
export const useRecordingMode = (): RecordingMode =>
  useServerConfig().data?.recording ?? "optional";
