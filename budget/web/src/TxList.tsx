import { useMemo } from "react";
import type { Category, Member, Tx } from "./api";
import { dayLabel, money, todayFrom } from "./format";
import { Who } from "./ui";

/**
 * Список трат, сгруппированный по дням.
 *
 * Получателей строка называет поимённо, а не «на двоих»: при десяти
 * участниках относительная подпись бессмысленна, а «на всех» — это отдельный
 * случай, который надо отличать от «на Аню и Улю».
 */
export function TxList({
  items,
  members,
  categories,
  slots,
  onPick,
}: {
  items: Tx[];
  members: Member[];
  categories: Category[];
  slots: Map<number, number>;
  onPick: (tx: Tx) => void;
}) {
  const byID = useMemo(() => new Map(members.map((m) => [m.id, m])), [members]);
  const catByID = useMemo(() => new Map(categories.map((c) => [c.id, c])), [categories]);

  const days = useMemo(() => {
    const out = new Map<string, Tx[]>();
    for (const tx of items) {
      // День берётся в часовом поясе устройства: сервер отдаёт момент
      // времени, а «какой это был день» человек читает по своим часам.
      const d = new Date(tx.spent_at);
      const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(
        d.getDate(),
      ).padStart(2, "0")}`;
      const list = out.get(key);
      if (list) list.push(tx);
      else out.set(key, [tx]);
    }
    return [...out.entries()];
  }, [items]);

  const today = todayFrom(days.map(([day]) => ({ day })));

  return (
    <div className="list">
      {days.map(([day, txs]) => (
        <section key={day} className="day">
          <h3 className="day__title">
            <span>{dayLabel(day, today)}</span>
            <b>{money(sum(txs))}</b>
          </h3>
          {txs.map((tx) => (
            <button key={tx.id} className="row" onClick={() => onPick(tx)}>
              <Who member={byID.get(tx.payer)} slot={slots.get(tx.payer) ?? 10} />
              <span className="row__main">
                <span className="row__desc">{tx.description || "Без описания"}</span>
                <span className="row__sub">
                  {catByID.get(tx.category_id ?? -1)?.name ?? "Без категории"}
                  {" · "}
                  {recipientsLabel(tx, byID)}
                  {tx.needs_review && <b className="row__flag"> · проверить</b>}
                  {tx.pending && <b className="row__flag"> · категория позже</b>}
                </span>
              </span>
              <span className={`row__amount${tx.kind !== "expense" ? " row__amount--other" : ""}`}>
                {tx.kind === "income" ? "↑ " : tx.kind === "transfer" ? "↔ " : ""}
                {money(tx.amount)}
              </span>
            </button>
          ))}
        </section>
      ))}
    </div>
  );
}

/** Кому досталась трата — именами. Пустой список означает «на всех». */
export function recipientsLabel(tx: Tx, byID: Map<number, Member>): string {
  if (tx.kind !== "expense") return tx.kind === "income" ? "поступление" : "перевод";
  if (tx.recipients.length === 0) return "на всех";

  const names = tx.recipients.map((id) => byID.get(id)?.name ?? "кто-то");
  // Больше двух имён в строку не влезает даже на широком телефоне.
  if (names.length > 2) return `${names[0]} и ещё ${names.length - 1}`;
  return names.join(" и ");
}

function sum(items: Tx[]): string {
  // Складываем в копейках целыми числами: суммировать деньги через float
  // нельзя даже ради заголовка дня.
  let cents = 0;
  for (const tx of items) {
    if (tx.kind !== "expense") continue;
    const [whole, frac = ""] = tx.amount.split(".");
    cents += Number(whole) * 100 + Number(frac.padEnd(2, "0").slice(0, 2));
  }
  const sign = cents < 0 ? "-" : "";
  const abs = Math.abs(cents);
  return `${sign}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, "0")}`;
}
