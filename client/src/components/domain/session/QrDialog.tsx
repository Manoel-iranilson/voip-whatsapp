import { Loader2 } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { useSessions } from "@/stores/sessions";

/** Shows the live QR for one session; it closes itself once the session pairs. */
export const QrDialog = ({ sid, onClose }: { sid: string | null; onClose: () => void }) => {
  const session = useSessions((s) => s.sessions.find((x) => x.id === sid) ?? null);
  const qr = useSessions((s) => (sid ? s.qrs[sid] : undefined));
  const open = !!session && !session.paired;

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>Conectar {session?.name}</DialogTitle>
          <DialogDescription>
            No celular: WhatsApp → Dispositivos conectados → Conectar dispositivo, e escaneie.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col items-center gap-3 py-2">
          {qr ? (
            <div className="rounded-lg border bg-white p-3">
              <QRCodeSVG value={qr} size={232} marginSize={1} />
            </div>
          ) : (
            <>
              <Skeleton className="h-[258px] w-[258px] rounded-lg" />
              <Badge variant="muted" className="gap-1.5">
                <Loader2 className="h-3 w-3 animate-spin" /> Aguardando QR…
              </Badge>
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
};
