import { useEffect, useState } from "react";
import { PhoneOff } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { attachMeter } from "@/lib/audio-meter";
import { setOutputDevice } from "@/lib/webrtc";
import { useCalls } from "@/stores/calls";
import { useDevices } from "@/stores/devices";
import { useEndCall } from "@/hooks/useEndCall";
import { formatCallDuration } from "@/utils/format";
import type { CallStatus, CallSummary } from "@/types/call";

const statusVariant: Record<CallStatus, "success" | "secondary" | "muted"> = {
  connected: "success",
  ringing: "secondary",
  starting: "secondary",
  ended: "muted",
};

const Meter = ({ label, db }: { label: string; db: number }) => {
  const pct = Math.max(0, Math.min(100, Math.round(((db + 60) / 60) * 100)));
  return (
    <div className="space-y-1">
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="h-2 overflow-hidden rounded-full bg-muted">
        <div className="h-full bg-primary transition-all" style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
};

export const CallCard = ({ call }: { call: CallSummary }) => {
  const conn = useCalls((s) => s.ownConnections.get(call.callId));
  const outDeviceId = useDevices((s) => s.outId);
  const endCall = useEndCall();
  const [, force] = useState(0);
  const [micDb, setMicDb] = useState(-60);
  const [peerDb, setPeerDb] = useState(-60);

  useEffect(() => {
    const t = setInterval(() => force((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    if (!conn) return;
    const offMic = attachMeter(conn.micAnalyser, setMicDb);
    const offPeer = attachMeter(conn.peerAnalyser, setPeerDb);
    return () => {
      offMic();
      offPeer();
    };
  }, [conn]);

  useEffect(() => {
    if (conn) setOutputDevice(outDeviceId);
  }, [outDeviceId, conn]);

  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="truncate font-medium">{call.peer}</p>
            <Badge variant={statusVariant[call.status]} className="mt-1">
              {formatCallDuration(call.startedAt, call.status)}
            </Badge>
          </div>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="destructive"
                size="icon"
                onClick={() => endCall.mutate({ sid: call.sessionId, callId: call.callId })}
                aria-label="End call"
              >
                <PhoneOff className="h-4 w-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>End call</TooltipContent>
          </Tooltip>
        </div>
        <Meter label="Mic" db={micDb} />
        <Meter label="Peer" db={peerDb} />
      </CardContent>
    </Card>
  );
};
