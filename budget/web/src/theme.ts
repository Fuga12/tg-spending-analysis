import { tg } from "./api";

/**
 * Оформление и платформа для AppRoot.
 *
 * Берутся у Telegram, а не выбираются нами: приложение живёт внутри клиента,
 * и светлая панель в тёмном Telegram выглядит чужой вставкой. Если SDK не
 * загрузился, ориентируемся на системную тему — это ближе к правде, чем
 * жёстко выбранная.
 */
export function appearance(): "light" | "dark" {
  if (tg?.colorScheme === "light" || tg?.colorScheme === "dark") return tg.colorScheme;
  return window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

/**
 * iOS и всё остальное Telegram рисует по-разному, и библиотека это повторяет.
 * Различать надо: на iPhone «base»-вид читается как чужое приложение.
 */
export function platform(): "ios" | "base" {
  const p = (tg as { platform?: string } | undefined)?.platform ?? "";
  return p === "ios" || p === "macos" ? "ios" : "base";
}
