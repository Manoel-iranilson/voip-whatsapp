import { useEffect } from "react";
import { Loader2, PlusCircle } from "lucide-react";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster } from "@/components/ui/sonner";
import { AppShell } from "@/components/layout/AppShell";
import { CallsPage } from "@/pages/CallsPage";
import { HistoryPage } from "@/pages/HistoryPage";
import { LoginPage } from "@/pages/LoginPage";
import { SessionsOverviewPage } from "@/pages/SessionsOverviewPage";
import { SessionPairing } from "@/components/domain/session/SessionPairing";
import { SessionHeader } from "@/components/domain/session/SessionHeader";
import { IncomingCallModal } from "@/components/domain/call/IncomingCallModal";
import { EmptyState } from "@/components/shared/EmptyState";
import { ensureSessionsWired, useSessions } from "@/stores/sessions";
import { ensureCallsWired } from "@/stores/calls";
import { checkAuth, useAuth } from "@/stores/auth";
import { useTheme } from "@/stores/theme";
import { useView } from "@/stores/view";

const CallsView = () => {
  const sessions = useSessions((s) => s.sessions);
  const activeId = useSessions((s) => s.activeId);
  const active = sessions.find((s) => s.id === activeId) ?? null;

  if (sessions.length === 0) {
    return (
      <EmptyState
        icon={<PlusCircle className="h-6 w-6" />}
        title="Nenhuma sessão ainda"
        description="Crie uma sessão na aba Sessões para começar a ligar."
      />
    );
  }
  if (!active) return <EmptyState title="Selecione uma sessão" description="Escolha uma sessão na lista ao lado." />;
  return (
    <div className="space-y-6">
      <SessionHeader session={active} />
      {active.paired ? <CallsPage sid={active.id} /> : <SessionPairing session={active} />}
    </div>
  );
};

export const App = () => {
  const status = useAuth((s) => s.status);
  const view = useView((s) => s.view);
  const theme = useTheme((s) => s.theme);

  useEffect(() => {
    void checkAuth();
  }, []);

  useEffect(() => {
    // The event stream carries the token from the moment it opens, so it waits
    // until the token is known to work.
    if (status !== "ok") return;
    ensureSessionsWired();
    ensureCallsWired();
  }, [status]);

  if (status === "checking") {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (status === "required") {
    return (
      <>
        <LoginPage />
        <Toaster theme={theme} position="top-right" richColors closeButton />
      </>
    );
  }

  return (
    <TooltipProvider delayDuration={200}>
      <AppShell>
        {view === "sessions" && <SessionsOverviewPage />}
        {view === "calls" && <CallsView />}
        {view === "history" && <HistoryPage />}
      </AppShell>
      <IncomingCallModal />
      <Toaster theme={theme} position="top-right" richColors closeButton />
    </TooltipProvider>
  );
};
