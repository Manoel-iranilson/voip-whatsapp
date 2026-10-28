import { useState } from "react";
import { Loader2, Phone, Plus, Power, QrCode, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { EmptyState } from "@/components/shared/EmptyState";
import { QrDialog } from "@/components/domain/session/QrDialog";
import { createSession, deleteSession, logoutSession, pairSession } from "@/services/sessions";
import { setActiveSession, useSessions } from "@/stores/sessions";
import { useCalls } from "@/stores/calls";
import { setView } from "@/stores/view";
import { jidNumber, statusLabel, statusVariant } from "@/utils/session";
import type { SessionInfo } from "@/types/session";

export const SessionsOverviewPage = () => {
  const sessions = useSessions((s) => s.sessions);
  const calls = useCalls((s) => s.calls);
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [qrFor, setQrFor] = useState<string | null>(null);
  const [toDelete, setToDelete] = useState<SessionInfo | null>(null);

  const run = async (id: string, fn: () => Promise<unknown>) => {
    setBusyId(id);
    try {
      await fn();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusyId(null);
    }
  };

  const onCreate = async () => {
    setCreating(true);
    try {
      const { id } = await createSession(name.trim() || "WhatsApp");
      setName("");
      setActiveSession(id);
      setQrFor(id);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setCreating(false);
    }
  };

  const connect = (s: SessionInfo) => {
    setQrFor(s.id);
    // A logged-out session needs a fresh pairing round before a QR shows up; one
    // already waiting on a QR just needs the dialog.
    if (s.state === "logged_out") void run(s.id, () => pairSession(s.id));
  };

  const openCalls = (s: SessionInfo) => {
    setActiveSession(s.id);
    setView("calls");
  };

  const connected = sessions.filter((s) => s.state === "open").length;

  return (
    <div className="mx-auto max-w-5xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Sessões</h1>
          <p className="text-sm text-muted-foreground">
            {sessions.length} no total · {connected} conectada{connected === 1 ? "" : "s"}
          </p>
        </div>
        <form
          className="flex w-full gap-2 sm:w-auto"
          onSubmit={(e) => {
            e.preventDefault();
            void onCreate();
          }}
        >
          <Input
            placeholder="Nome da nova sessão"
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="sm:w-56"
          />
          <Button type="submit" disabled={creating}>
            {creating ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
            Nova sessão
          </Button>
        </form>
      </div>

      {sessions.length === 0 ? (
        <EmptyState
          icon={<Plus className="h-6 w-6" />}
          title="Nenhuma sessão ainda"
          description="Crie uma sessão e escaneie o QR com o WhatsApp para começar a ligar."
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {sessions.map((s) => {
            const busy = busyId === s.id;
            const active = calls.filter((c) => c.sessionId === s.id && c.status !== "ended").length;
            return (
              <Card key={s.id}>
                <CardHeader className="space-y-1 pb-3">
                  <div className="flex items-start justify-between gap-2">
                    <CardTitle className="truncate text-base">{s.name}</CardTitle>
                    <Badge variant={statusVariant[s.state]}>{statusLabel[s.state]}</Badge>
                  </div>
                  <p className="truncate text-sm text-muted-foreground">
                    {s.jid ? `+${jidNumber(s.jid)}` : "Sem número vinculado"}
                    {active > 0 && ` · ${active} em ligação`}
                  </p>
                </CardHeader>
                <CardContent className="flex flex-wrap gap-2">
                  {s.paired ? (
                    <>
                      <Button size="sm" onClick={() => openCalls(s)}>
                        <Phone className="h-4 w-4" />
                        Ligar
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() => run(s.id, () => logoutSession(s.id))}
                      >
                        {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Power className="h-4 w-4" />}
                        Desconectar
                      </Button>
                    </>
                  ) : (
                    <Button size="sm" disabled={busy} onClick={() => connect(s)}>
                      {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <QrCode className="h-4 w-4" />}
                      Conectar
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    className="ml-auto text-muted-foreground hover:text-destructive"
                    onClick={() => setToDelete(s)}
                    aria-label={`Excluir ${s.name}`}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}

      <QrDialog sid={qrFor} onClose={() => setQrFor(null)} />
      <ConfirmDialog
        open={!!toDelete}
        onOpenChange={(o) => !o && setToDelete(null)}
        title="Excluir sessão?"
        description={toDelete ? `${toDelete.name} será desconectada e removida.` : undefined}
        confirmLabel="Excluir"
        destructive
        onConfirm={() => {
          if (toDelete) void run(toDelete.id, () => deleteSession(toDelete.id));
        }}
      />
    </div>
  );
};
