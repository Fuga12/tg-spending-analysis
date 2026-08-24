import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Собранный фронт кладётся прямо в internal/app/dist — оттуда его забирает
// embed.FS, и бинарь остаётся самодостаточным: деплой это один файл, а
// каталог, который забыли скопировать, даёт белый экран без ошибок в логе.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/app/dist",
    emptyOutDir: true,
  },
  server: {
    // В разработке API берём у настоящего сервера: подписи Telegram в
    // браузере нет, и внутрь пускает APP_DEV_USER_ID.
    proxy: {
      "/api": "http://127.0.0.1:8081",
    },
  },
});
