import { Loader2 } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { useSessions } from "@/stores/sessions";
import type { SessionInfo } from "@/types/session";

export const SessionPairing = ({ session }: { session: SessionInfo }) => {
  const qr = useSessions((s) => s.qrs[session.id]);

  return (
    <div className="flex min-h-[55vh] items-center justify-center">
      <Card className="w-full max-w-md">
        <CardHeader className="items-center text-center">
          <CardTitle>Conectar {session.name}</CardTitle>
          <CardDescription>
            No celular: WhatsApp → Dispositivos conectados → Conectar dispositivo, e escaneie.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col items-center gap-4">
          {qr ? (
            <div className="rounded-lg border bg-white p-3">
              <QRCodeSVG value={qr} size={232} marginSize={1} />
            </div>
          ) : session.state === "logged_out" ? (
            <Badge variant="destructive">Desconectado — clique em Conectar acima para gerar o QR</Badge>
          ) : (
            <>
              <Skeleton className="h-[258px] w-[258px] rounded-lg" />
              <Badge variant="muted" className="gap-1.5">
                <Loader2 className="h-3 w-3 animate-spin" /> Aguardando QR…
              </Badge>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
};
