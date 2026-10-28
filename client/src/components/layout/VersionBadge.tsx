import { useQuery } from "@tanstack/react-query";
import { apiGet } from "@/lib/api";

type ServerVersion = { version: string; commit: string; buildTime: string; startedAt: string };

const fmt = (iso: string) => (iso ? new Date(iso).toLocaleString("pt-BR") : "—");

/** The live server build, so a deploy can be confirmed from the panel itself. */
export const VersionBadge = () => {
  const { data } = useQuery({
    queryKey: ["version"],
    queryFn: () => apiGet<ServerVersion>("/api/version"),
    staleTime: Infinity,
    retry: false,
  });
  if (!data) return null;

  const label = data.commit !== "unknown" ? data.commit : data.version;
  return (
    <span
      className="hidden font-mono text-xs text-muted-foreground lg:inline"
      title={`Versão: ${data.version}\nCommit: ${data.commit}\nBuild: ${fmt(data.buildTime)}\nIniciado: ${fmt(data.startedAt)}`}
    >
      {label}
      {data.buildTime && ` · build ${fmt(data.buildTime)}`}
    </span>
  );
};
