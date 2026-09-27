import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

// The dashboard calls the gateway's management API with relative paths
// (/admin/...). The dev and preview servers proxy them to the gateway, so the
// browser sees a single origin and the gateway needs no CORS support.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const target = env.GATEWAY_URL ?? "http://localhost:8080";
  const proxy = { "/admin": { target, changeOrigin: true } };
  return {
    plugins: [react()],
    server: { port: Number(env.PORT ?? 5173), proxy },
    preview: { port: Number(env.PORT ?? 4173), proxy },
  };
});
