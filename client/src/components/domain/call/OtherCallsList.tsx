import { Phone, PhoneIncoming } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { usePickUpCall } from "@/hooks/usePickUpCall";
import { useDevices } from "@/stores/devices";
import { formatCallDuration } from "@/utils/format";
import type { CallSummary } from "@/types/call";

export const OtherCallsList = ({ sid, calls }: { sid: string; calls: CallSummary[] }) => {
  const micId = useDevices((s) => s.micId);
  const pickUp = usePickUpCall(micId);

  if (calls.length === 0) return null;
  return (
    <section className="space-y-3">
      <h2 className="text-sm font-medium text-muted-foreground">Other active calls</h2>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {calls.map((c) => {
          // A call with no owner was started by another system through the API and
          // is waiting for an operator to take the audio.
          const unclaimed = c.owner === null;
          return (
            <Card key={c.callId} className={unclaimed ? undefined : "opacity-90"}>
              <CardContent className="flex items-center gap-3 p-3">
                <span className="flex h-9 w-9 items-center justify-center rounded-full bg-muted text-muted-foreground">
                  {unclaimed ? <PhoneIncoming className="h-4 w-4" /> : <Phone className="h-4 w-4" />}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{c.peer}</p>
                  <p className="text-xs text-muted-foreground">
                    {unclaimed ? "waiting for an operator" : c.direction}
                  </p>
                </div>
                {unclaimed ? (
                  <Button
                    size="sm"
                    disabled={pickUp.isPending}
                    onClick={() => pickUp.mutate({ sid, callId: c.callId })}
                  >
                    Pick up
                  </Button>
                ) : (
                  <Badge variant="muted">{formatCallDuration(c.startedAt, c.status)}</Badge>
                )}
              </CardContent>
            </Card>
          );
        })}
      </div>
    </section>
  );
};
