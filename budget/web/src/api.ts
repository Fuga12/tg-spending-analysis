// Клиент API. Деньги — строками: в JSON число это float64, и через float
// деньги гонять нельзя.

export type Member = {
  id: number;
  user_id: number;
  name: string;
  role: "admin" | "member";
  left?: boolean;
  avatar_at?: string;
};

export type Category = {
  id: number;
  name: string;
  hint: string;
  template_key?: string;
  default_to: number | null;
  sort_order: number;
};

export type Invite = {
  id: number;
  group_name: string;
  inviter: string;
  expires_at: string;
};

export type Group = {
  id: number;
  name: string;
  /** Про смотрящего: по ним решается, показывать ли «пригласить». */
  member_id: number;
  role: "admin" | "member";
};

export type State = {
  me: { id: number; name: string; avatar_at?: string };
  group: Group | null;
  members: Member[];
  categories: Category[];
  invites: Invite[];
  max_members: number;
};

export type Tx = {
  id: number;
  payer: number;
  /** Пустой список означает «на всю группу», а не «неизвестно». */
  recipients: number[];
  kind: "expense" | "income" | "transfer";
  amount: string;
  description: string;
  category_id: number | null;
  raw_text: string;
  spent_at: string;
  updated_at?: string;
  needs_review?: boolean;
  pending?: boolean;
};

export type Line = { id: number; key: string; name: string; amount: string; percent: number };

export type MonthReport = {
  year: number;
  month: number;
  total: string;
  categories: Line[];
  payers: Line[];
  beneficiaries: Line[];
  compare: { previous: string; current: string; days: number; partial: boolean } | null;
  review: number;
};

export type DayPoint = { day: string; amount: string };
export type MonthPoint = { year: number; month: number; amount: string };

/** Отказ сервера с текстом, который можно показать человеку. */
export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

type Telegram = {
  initData: string;
  ready: () => void;
  expand: () => void;
  colorScheme?: string;
  HapticFeedback?: { impactOccurred: (style: string) => void };
  showConfirm?: (message: string, cb: (ok: boolean) => void) => void;
};

/** SDK Telegram, если он загрузился. */
export const tg: Telegram | undefined = (window as any).Telegram?.WebApp;

/**
 * Подпись Telegram: сначала из SDK, при его отсутствии — из адреса страницы.
 *
 * Второй путь не «на всякий случай». SDK грузится с telegram.org, и причин
 * не доехать у него хватает: медленная сеть, блокировка, кэш вебвью со старой
 * версией страницы. Тогда window.Telegram не появляется вовсе — а подпись всё
 * это время лежит в адресе: Telegram дописывает её в hash при открытии
 * Mini App (#tgWebAppData=…). Полагаться только на скрипт значит поставить
 * единственную дверь в зависимость от чужого CDN.
 *
 * URLSearchParams декодирует значение один раз — ровно до того вида, в каком
 * его подписали.
 */
export function initData(): string {
  if (tg?.initData) return tg.initData;
  const hash = window.location.hash.replace(/^#/, "");
  return hash ? (new URLSearchParams(hash).get("tgWebAppData") ?? "") : "";
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const signature = initData();
  if (!signature) {
    // Ходить в сеть незачем: сервер ответит 401, а сказать человеку надо
    // не «открой из Telegram» — он оттуда и открыл, — а что подписи нет.
    throw new ApiError(0, "Приложение открыто без подписи Telegram. Закрой его и открой заново из бота.");
  }

  const headers: Record<string, string> = {
    // Подпись едет заголовком, а не параметром адреса: в адресе она осела бы
    // в логах прокси и в истории браузера.
    Authorization: `tma ${signature}`,
  };
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (res.status === 204) return undefined as T;
  const text = await res.text();
  if (!res.ok) {
    let message = "Что-то пошло не так.";
    try {
      message = JSON.parse(text).error ?? message;
    } catch {
      /* сервер ответил не JSON — показываем общую фразу */
    }
    throw new ApiError(res.status, message);
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

const qs = (params: Record<string, string | number | undefined>) => {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "" && v !== 0) q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
};

export type TxFilter = {
  from?: string;
  to?: string;
  payer?: number;
  /** Число — участник, "common" — трата на всю группу. */
  recipient?: number | "common";
  category?: number;
  kind?: string;
  pending?: boolean;
  q?: string;
  limit?: number;
  offset?: number;
};

export const api = {
  state: () => call<State>("GET", "/api/state"),

  setName: (name: string) => call<void>("POST", "/api/me/name", { name }),
  clearAvatar: () => call<void>("DELETE", "/api/me/avatar"),
  setAvatar: async (blob: Blob) => {
    const headers: Record<string, string> = { "Content-Type": "image/jpeg" };
    const signature = initData();
    if (signature) headers.Authorization = `tma ${signature}`;
    const res = await fetch("/api/me/avatar", { method: "POST", headers, body: blob });
    if (!res.ok) throw new ApiError(res.status, "Не смог сохранить фото.");
    return (await res.json()) as { avatar_at: string };
  },
  avatarURL: (userID: number, version?: string) =>
    `/api/avatar/${userID}${version ? `?v=${encodeURIComponent(version)}` : ""}`,

  createGroup: (name: string) => call<Group>("POST", "/api/group", { name }),
  invite: (userID: number) => call<Invite>("POST", "/api/group/invite", { user_id: userID }),
  leave: () => call<void>("POST", "/api/group/leave"),
  setRole: (memberID: number, role: string) =>
    call<void>("PATCH", `/api/group/members/${memberID}`, { role }),
  removeMember: (memberID: number) => call<void>("DELETE", `/api/group/members/${memberID}`),

  acceptInvite: (id: number) => call<{ group_id: number }>("POST", `/api/invites/${id}/accept`),
  declineInvite: (id: number) => call<void>("POST", `/api/invites/${id}/decline`),

  transactions: (f: TxFilter) =>
    call<{ items: Tx[]; total: number }>("GET", `/api/transactions${qs(f as any)}`),
  updateTx: (id: number, patch: Record<string, unknown>) =>
    call<Tx>("PATCH", `/api/transactions/${id}`, patch),
  deleteTx: (id: number) => call<void>("DELETE", `/api/transactions/${id}`),
  restoreTx: (id: number) => call<Tx>("POST", `/api/transactions/${id}/restore`),

  createCategory: (name: string, hint: string, defaultTo: number | null) =>
    call<Category>("POST", "/api/categories", { name, hint, default_to: defaultTo }),
  updateCategory: (id: number, name: string, hint: string, defaultTo: number | null) =>
    call<void>("PATCH", `/api/categories/${id}`, { name, hint, default_to: defaultTo }),

  month: (year: number, month: number) =>
    call<MonthReport>("GET", `/api/report/month${qs({ year, month })}`),
  days: (year: number, month: number) =>
    call<DayPoint[]>("GET", `/api/report/days${qs({ year, month })}`),
  months: () => call<MonthPoint[]>("GET", "/api/report/months"),
};
