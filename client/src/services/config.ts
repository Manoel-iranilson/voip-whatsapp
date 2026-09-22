import { apiGet } from "@/lib/api";
import type { ServerConfig } from "@/types/config";

export const fetchConfig = () => apiGet<ServerConfig>("/api/config");
