import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { App } from "./App";
import { queryClient } from "@/lib/query";
import { installAudioUnlock } from "@/lib/audio-context";
import "@/styles/index.css";

// Safari only starts audio from a user gesture; see audio-context.ts.
installAudioUnlock();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
