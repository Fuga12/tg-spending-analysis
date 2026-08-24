import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { AppRoot } from "@telegram-apps/telegram-ui";
import "@telegram-apps/telegram-ui/dist/styles.css";
import App from "./App";
import { appearance, platform } from "./theme";
import "./styles.css";

// AppRoot раздаёт всему дереву оформление Telegram: цвета, скругления, шрифт.
// Без него компоненты библиотеки рисуются наугад. Свой styles.css идёт после
// библиотечного — он дописывает только то, чего в ней нет: графики.
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppRoot appearance={appearance()} platform={platform()}>
      <App />
    </AppRoot>
  </StrictMode>,
);
