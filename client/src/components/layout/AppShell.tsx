import { useState, type ReactNode } from "react";
import { History, LayoutGrid, LogOut, Menu, Phone, PhoneCall } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import { getAuthToken } from "@/lib/auth-token";
import { logout } from "@/stores/auth";
import { setView, useView, type View } from "@/stores/view";
import { Sidebar } from "./Sidebar";
import { ThemeToggle } from "./ThemeToggle";

const tabs: { view: View; label: string; icon: typeof Phone }[] = [
  { view: "sessions", label: "Sessões", icon: LayoutGrid },
  { view: "calls", label: "Ligações", icon: Phone },
  { view: "history", label: "Histórico", icon: History },
];

export const AppShell = ({ children }: { children: ReactNode }) => {
  const [mobileOpen, setMobileOpen] = useState(false);
  const view = useView((s) => s.view);
  // The account list only matters on the calls screen, where it picks who dials.
  const showSidebar = view === "calls";

  return (
    <div className="flex min-h-screen flex-col">
      <header className="sticky top-0 z-30 flex items-center justify-between gap-2 border-b bg-background/80 px-4 py-3 backdrop-blur sm:px-6">
        <div className="flex items-center gap-2">
          {showSidebar && (
            <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
              <SheetTrigger asChild>
                <Button variant="outline" size="icon" className="md:hidden" aria-label="Sessões">
                  <Menu className="h-4 w-4" />
                </Button>
              </SheetTrigger>
              <SheetContent side="left" className="w-72 p-0">
                <SheetTitle className="px-3 pt-3">Sessões</SheetTitle>
                <Sidebar onNavigate={() => setMobileOpen(false)} />
              </SheetContent>
            </Sheet>
          )}
          <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
            <PhoneCall className="h-4 w-4" />
          </span>
          <span className="hidden text-lg font-semibold tracking-tight sm:inline">WaCalls</span>
        </div>
        <nav className="flex items-center gap-1">
          {tabs.map(({ view: v, label, icon: Icon }) => (
            <Button
              key={v}
              variant="ghost"
              size="sm"
              onClick={() => setView(v)}
              className={cn(view === v && "bg-accent text-accent-foreground")}
              aria-current={view === v ? "page" : undefined}
            >
              <Icon className="h-4 w-4" />
              <span className="hidden sm:inline">{label}</span>
            </Button>
          ))}
        </nav>
        <div className="flex items-center gap-1">
          <ThemeToggle />
          {getAuthToken() && (
            <Button variant="ghost" size="icon" onClick={logout} aria-label="Sair" title="Sair">
              <LogOut className="h-4 w-4" />
            </Button>
          )}
        </div>
      </header>
      <div className="flex flex-1">
        {showSidebar && (
          <aside className="hidden w-64 shrink-0 border-r md:block">
            <Sidebar />
          </aside>
        )}
        <main className="min-w-0 flex-1 px-4 py-6 sm:px-6">{children}</main>
      </div>
    </div>
  );
};
