import { useState } from "react";
import { Download, Loader2, PhoneIncoming, PhoneOutgoing, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/shared/EmptyState";
import { useAllHistory } from "@/hooks/useHistory";
import { recordingUrl } from "@/services/history";
import { useSessions } from "@/stores/sessions";
import type { HistoryRow } from "@/types/history";

const duration = (r: HistoryRow): string => {
  if (!r.endedAt) return "—";
  const s = Math.max(0, Math.round((r.endedAt - r.startedAt) / 1000));
  return `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
};

export const HistoryPage = () => {
  const sessions = useSessions((s) => s.sessions);
  const [sid, setSid] = useState<string | null>(null);
  const { data: rows = [], isFetching, refetch } = useAllHistory(sid);
  const nameOf = (id: string) => sessions.find((s) => s.id === id)?.name ?? id;

  return (
    <div className="mx-auto max-w-5xl space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Histórico de ligações</h1>
          <p className="text-sm text-muted-foreground">{rows.length} ligação(ões) encerrada(s)</p>
        </div>
        <div className="flex gap-2">
          <select
            className="h-9 rounded-md border border-input bg-background px-3 text-sm"
            value={sid ?? ""}
            onChange={(e) => setSid(e.target.value || null)}
            aria-label="Filtrar por sessão"
          >
            <option value="">Todas as sessões</option>
            {sessions.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
          <Button variant="outline" size="sm" className="h-9" onClick={() => void refetch()} disabled={isFetching}>
            {isFetching ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
            Atualizar
          </Button>
        </div>
      </div>

      {rows.length === 0 ? (
        <EmptyState title="Nenhuma ligação ainda" description="As ligações feitas e recebidas aparecem aqui." />
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">Sessão</th>
                <th className="px-3 py-2 font-medium">Número</th>
                <th className="px-3 py-2 font-medium">Início</th>
                <th className="px-3 py-2 font-medium">Duração</th>
                <th className="px-3 py-2 font-medium">Encerramento</th>
                <th className="px-3 py-2 font-medium">Gravação</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.sessionId}:${r.callId}`} className="border-t">
                  <td className="whitespace-nowrap px-3 py-2">{nameOf(r.sessionId)}</td>
                  <td className="whitespace-nowrap px-3 py-2">
                    <span className="inline-flex items-center gap-1.5">
                      {r.direction === "inbound" ? (
                        <PhoneIncoming className="h-3.5 w-3.5 text-muted-foreground" aria-label="Recebida" />
                      ) : (
                        <PhoneOutgoing className="h-3.5 w-3.5 text-muted-foreground" aria-label="Feita" />
                      )}
                      {r.peer.split("@")[0]}
                    </span>
                  </td>
                  <td className="whitespace-nowrap px-3 py-2">{new Date(r.startedAt).toLocaleString("pt-BR")}</td>
                  <td className="whitespace-nowrap px-3 py-2 tabular-nums">{duration(r)}</td>
                  <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">{r.endReason || "—"}</td>
                  <td className="whitespace-nowrap px-3 py-2">
                    {r.recording ? (
                      <a
                        href={recordingUrl(r)}
                        className="inline-flex items-center gap-1 text-primary hover:underline"
                        download
                      >
                        <Download className="h-3.5 w-3.5" />
                        WAV
                      </a>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
};
