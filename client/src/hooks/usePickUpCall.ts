import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { openCall } from "@/lib/webrtc";
import { endCall } from "@/services/calls";
import { registerOwnConnection } from "@/stores/calls";

/**
 * Takes over a call that nobody owns — typically one started by another system
 * through the API.
 *
 * There is no separate claim request: posting the SDP offer is what claims the
 * call, so opening the WebRTC connection is the whole operation.
 */
export const usePickUpCall = (micId: string | null) =>
  useMutation({
    mutationFn: async (vars: { sid: string; callId: string }) => {
      const conn = await openCall(vars.sid, vars.callId, micId);
      registerOwnConnection(vars.callId, conn);
      return vars.callId;
    },
    onError: (e: Error, vars) => {
      if (e.message.includes("409")) {
        toast.error("Another operator picked up this call first.");
        return;
      }
      // The call is ours now but we cannot carry the audio, so hand it back
      // rather than leaving it stuck on a browser that has no microphone.
      void endCall(vars.sid, vars.callId).catch(() => {});
      toast.error(e.message);
    },
  });
