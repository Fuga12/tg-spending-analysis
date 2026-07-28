// Обёртка над fetch. Знает про 401 (увести на экран входа) и про то, что
// под /api всё отвечает JSON.

export class Unauthorized extends Error {}

export type Me = {
  id: number;
  name: string;
  partner: { id: number; name: string } | null;
};

export type Tx = {
  id: number;
  day: string;
  amount: string;
  description: string;
  category_id: number | null;
  category: string;
  payer_id: number;
  beneficiary: "payer" | "partner" | "both";
  kind: "expense" | "income" | "transfer";
  spent_at: string;
  raw_text: string;
  needs_review: boolean;
  updated_at: string | null;
  mine: boolean;
};

export type TxPage = { items: Tx[]; total: number; has_more: boolean };

export type Line = { id: number; name: string; amount: string; percent: number };

export type MonthReport = {
  year: number;
  month: number;
  total: string;
  compare: { days: number; previous: string; percent: number; partial: boolean } | null;
  categories: Line[];
  payers: Line[];
  beneficiaries: Line[];
  pending: number;
};

async function get<T>(path: string): Promise<T> {
  const resp = await fetch(path, { credentials: "same-origin" });
  if (resp.status === 401) throw new Unauthorized("нужен вход");
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({ error: "" }));
    throw new Error(body.error || `ошибка ${resp.status}`);
  }
  return resp.json() as Promise<T>;
}

export const api = {
  me: () => get<Me>("/api/me"),

  month: (year: number, month: number) =>
    get<MonthReport>(`/api/report/month?year=${year}&month=${month}`),

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
