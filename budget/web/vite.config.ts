import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Собранный фронт кладётся прямо в internal/web/dist — оттуда его забирает
// embed.FS, и бинарь остаётся самодостаточным (webapp.md §0).
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
  },
  server: {
    // В режиме разработки API берём у настоящего сервера.
    proxy: {
      "/api": "http://127.0.0.1:8081",
      "/auth": "http://127.0.0.1:8081",
    },
  },
});
