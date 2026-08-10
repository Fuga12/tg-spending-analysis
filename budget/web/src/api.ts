// Обёртка над fetch. Знает про 401 (увести на экран входа) и про то, что
// под /api всё отвечает JSON.

export class Unauthorized extends Error {}

/** 409: запись изменили, пока её правили. Несёт текущее состояние. */
export class Conflict extends Error {
  constructor(public current: Tx) {
    super("запись изменилась");
  }
}

export type Beneficiary = string;

export type BeneficiaryGroup = {
  id: number;
  name: string;
  key: string;
};

export type Category = {
  id: number;
  name: string;
  beneficiary: Beneficiary;
  hint: string;
  /** Адресат-человек. Заполнен — перебивает beneficiary. */
  user_id: number | null;
};

export type Person = {
  id: number;
  name: string;
  dative: string;
  /** Метка своего фото; пустая — своего нет, аватарка берётся у Telegram. */
  avatar_version: string;
};

export type Me = {
  id: number;
  name: string;
  /** «Илье», «Уле» — падеж считает сервер, чтобы правила жили в одном месте. */
  dative: string;
  avatar_version: string;
  partner: Person | null;
};

export type Tx = {
  id: number;
  day: string;
  amount: string;
  description: string;
  category_id: number | null;
  category: string;
  payer_id: number;
  beneficiary: string;
  kind: "expense" | "income" | "transfer";
  spent_at: string;
  raw_text: string;
  needs_review: boolean;
  updated_at: string | null;
  mine: boolean;
};

export type TxPage = { items: Tx[]; total: number; has_more: boolean };

export type Line = { id: number; key: string; name: string; amount: string; percent: number; delta?: string };

export type DayPoint = { day: string; amount: string };
export type MonthPoint = { year: number; month: number; amount: string };

export type Compare = {
  days: number;
  previous: string;
  percent: number;
  has_percent: boolean;
  difference: string;
  partial: boolean;
};

export type MonthReport = {
  year: number;
  month: number;
  total: string;
	recipient_total?: string;
  compare: Compare | null;
  categories: Line[];
  payers: Line[];
  beneficiaries: Line[];
  pending: number;
};

async function get<T>(path: string): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(path, { credentials: "same-origin" });
  } catch {
    // Сообщение браузера («Failed to fetch») человеку ничего не говорит.
    throw new Error("Сервер не отвечает");
  }
  if (resp.status === 401) throw new Unauthorized("нужен вход");
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({ error: "" }));
    throw new Error(body.error || `ошибка ${resp.status}`);
  }
  return resp.json() as Promise<T>;
}

async function send<T>(method: string, path: string, body: unknown): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new Error("Сервер не отвечает");
  }
  if (resp.status === 401) throw new Unauthorized("нужен вход");

  const text = await resp.text();
  const data = text ? JSON.parse(text) : {};
  if (resp.status === 409 && data.current) throw new Conflict(data.current as Tx);
  if (!resp.ok) throw new Error(data.error || `ошибка ${resp.status}`);
  return data as T;
}

export const api = {
  me: () => get<Me>("/api/me"),

  categories: () => get<Category[]>("/api/categories"),

  beneficiaryGroups: () => get<BeneficiaryGroup[]>("/api/beneficiary-groups"),

  createBeneficiaryGroup: (name: string) =>
    send<BeneficiaryGroup>("POST", "/api/beneficiary-groups", { name }),

  patchBeneficiaryGroup: (id: number, name: string) =>
    send<BeneficiaryGroup>("PATCH", `/api/beneficiary-groups/${id}`, { name }),

  deleteBeneficiaryGroup: (id: number) =>
    send<{ ok: boolean }>("DELETE", `/api/beneficiary-groups/${id}`, undefined),

  patchCategory: (
    id: number,
    body: { name: string; hint: string; beneficiary: string; user_id: number | null },
  ) =>
    send<Category>("PATCH", `/api/categories/${id}`, body),

  createCategory: (body: {
    name: string;
    hint: string;
    beneficiary: string;
    user_id: number | null;
  }) =>
    send<Category>("POST", "/api/categories", body),

  /** Своё фото профиля: base64 из canvas. Телеграмным не перебивается. */
  setAvatar: (photo: string) =>
    send<{ avatar_version: string }>("PUT", "/api/avatar", { photo }),

  clearAvatar: () => send<{ avatar_version: string }>("DELETE", "/api/avatar", undefined),

  daily: (year: number, month: number) =>
    get<DayPoint[]>(`/api/report/daily?year=${year}&month=${month}`),

  months: () => get<MonthPoint[]>("/api/report/months"),

  patch: (id: number, body: Record<string, unknown>) =>
    send<Tx>("PATCH", `/api/transactions/${id}`, body),

  create: (body: Record<string, unknown>) => send<Tx>("POST", "/api/transactions", body),

  remove: (id: number) => send<{ ok: boolean }>("DELETE", `/api/transactions/${id}`, undefined),

  restore: (id: number) => send<Tx>("PATCH", `/api/transactions/${id}`, { deleted: false }),

  month: (year: number, month: number, recipient = "") => {
	const q = new URLSearchParams({ year: String(year), month: String(month) });
	if (recipient) q.set("recipient", recipient);
	return get<MonthReport>(`/api/report/month?${q}`);
  },

  transactions: (params: Record<string, string | number | undefined>) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") q.set(k, String(v));
    }
    return get<TxPage>(`/api/transactions?${q}`);
  },

  logout: async (everywhere = false) => {
    await fetch(`/api/session${everywhere ? "?all=1" : ""}`, {
      method: "DELETE",
      credentials: "same-origin",
    });
  },
};
