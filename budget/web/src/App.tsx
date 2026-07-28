import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, Me, MonthReport, Tx, Unauthorized } from "./api";
import { beneficiaryLabel, dayLabel, initials, money, monthName, todayFrom } from "./format";

const PAGE = 200;

type Route = { year: number; month: number; query: string; pending: boolean };

/** Разбор хэша. Полноценный роутер ради трёх состояний — лишняя зависимость. */
function parseHash(): Route {
  const now = new Date();
  const raw = window.location.hash.replace(/^#\/?/, "");
  const [path, search = ""] = raw.split("?");
  const params = new URLSearchParams(search);

  const match = /^m\/(\d{4})-(\d{2})$/.exec(path);
  return {
    year: match ? Number(match[1]) : now.getFullYear(),
    month: match ? Number(match[2]) : now.getMonth() + 1,
    query: params.get("q") ?? "",
    pending: params.get("pending") === "1",
  };
}

function hashFor(r: Route): string {
  const params = new URLSearchParams();
  if (r.query) params.set("q", r.query);
  if (r.pending) params.set("pending", "1");
  const tail = params.toString();
  return `#/m/${r.year}-${String(r.month).padStart(2, "0")}${tail ? "?" + tail : ""}`;
}

export default function App() {
  const [route, setRoute] = useState<Route>(parseHash);
  const [me, setMe] = useState<Me | null>(null);
  const [report, setReport] = useState<MonthReport | null>(null);
  const [items, setItems] = useState<Tx[]>([]);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expired, setExpired] = useState(false);
  const [searching, setSearching] = useState(false);
  const [stuck, setStuck] = useState(false);

  const searchInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const onHash = () => setRoute(parseHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    const onScroll = () => setStuck(window.scrollY > 40);
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  // Смена месяца — replaceState: иначе «назад» после пяти листаний
  // возвращает по одному месяцу вместо выхода.
  const go = useCallback((next: Route, push = false) => {
    const hash = hashFor(next);
    if (push) window.location.hash = hash;
    else window.history.replaceState(null, "", hash);
    setRoute(next);
  }, []);

  useEffect(() => {
    api.me().then(setMe).catch(handleAuthError);
  }, []);

  function handleAuthError(err: unknown) {
    if (err instanceof Unauthorized) setExpired(true);
    else setError(err instanceof Error ? err.message : "что-то сломалось");
  }

  const load = useCallback(async () => {
    // При смене месяца прошлый экран приглушается, а не мигает скелетом.
    if (report) setRefreshing(true);
    setError(null);

    try {
      const listParams = route.query
        ? { q: route.query, limit: PAGE }
        : {
            year: route.year,
            month: route.month,
            limit: PAGE,
            pending: route.pending ? 1 : undefined,
          };

      const [monthData, page] = await Promise.all([
        route.query ? Promise.resolve(null) : api.month(route.year, route.month),
        api.transactions(listParams),
      ]);

      if (monthData) setReport(monthData);
      setItems(page.items);
      setTotal(page.total);
      setHasMore(page.has_more);
    } catch (err) {
      handleAuthError(err);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [route.year, route.month, route.query, route.pending]); // eslint-disable-line

  useEffect(() => {
    void load();
  }, [load]);

  async function loadMore() {
    try {
      const page = await api.transactions({
        year: route.query ? undefined : route.year,
        month: route.query ? undefined : route.month,
        q: route.query || undefined,
        pending: route.pending ? 1 : undefined,
        limit: PAGE,
        offset: items.length,
      });
      setItems((prev) => [...prev, ...page.items]);
      setHasMore(page.has_more);
    } catch (err) {
      handleAuthError(err);
    }
  }

  const today = useMemo(() => todayFrom(items), [items]);
  const grouped = useMemo(() => groupByDay(items), [items]);

  if (expired) return <Expired />;

  const shiftMonth = (delta: number) => {
    const d = new Date(route.year, route.month - 1 + delta, 1);
    go({ ...route, query: "", year: d.getFullYear(), month: d.getMonth() + 1 });
  };

  const now = new Date();
  const isCurrentMonth = route.year === now.getFullYear() && route.month === now.getMonth() + 1;

  return (
    <div className="app">
      <header className={`head${stuck ? " head--stuck" : ""}`}>
        {searching ? (
          <>
            <button className="iconbtn" onClick={() => { setSearching(false); go({ ...route, query: "" }); }}>
              ✕
            </button>
            <div className="search" style={{ flex: 1, paddingBottom: 0 }}>
              <input
                ref={searchInput}
                autoFocus
                placeholder="Описание или сумма"
                defaultValue={route.query}
                onKeyDown={(e) => {
                  if (e.key === "Enter") go({ ...route, query: e.currentTarget.value }, true);
                  if (e.key === "Escape") { setSearching(false); go({ ...route, query: "" }); }
                }}
              />
            </div>
          </>
        ) : (
          <>
            <button className="iconbtn" onClick={() => shiftMonth(-1)} aria-label="Предыдущий месяц">
              ‹
            </button>
            <div className="head__title">
              {stuck && report ? (
                <>
                  {monthName(route.month)} <span className="head__sum">{money(report.total)}</span>
                </>
              ) : (
                `${monthName(route.month)} ${route.year}`
              )}
            </div>
            <button
              className="iconbtn"
              onClick={() => shiftMonth(1)}
              disabled={isCurrentMonth}
              aria-label="Следующий месяц"
            >
              ›
            </button>
            <button className="iconbtn" onClick={() => setSearching(true)} aria-label="Поиск">
              🔍
            </button>
          </>
        )}
      </header>

      {error && (
        <div className="notice" role="alert">
          <span>⚠</span>
          <span style={{ flex: 1 }}>{error}</span>
          <button className="iconbtn" onClick={() => void load()}>↻</button>
        </div>
      )}

      {loading ? (
        <Skeleton />
      ) : (
        <div className={refreshing ? "fade" : undefined}>
          {route.query ? (
            <SearchSummary query={route.query} total={total} />
          ) : (
            report && <Hero report={report} />
          )}

          {!route.query && report && report.pending > 0 && !route.pending && (
            <button className="notice" onClick={() => go({ ...route, pending: true }, true)}>
              <span>⚠</span>
              <span style={{ flex: 1, textAlign: "left" }}>
                {report.pending} {plural(report.pending, "запись разобрана", "записи разобраны", "записей разобрано")}{" "}
                вслепую
              </span>
              <span>Проверить →</span>
            </button>
          )}

          {route.pending && (
            <div className="foot" style={{ paddingTop: 8 }}>
              На проверку · {total}
              <button onClick={() => go({ ...route, pending: false }, true)}>снять фильтр</button>
            </div>
          )}

          {items.length === 0 ? (
            <Empty query={route.query} />
          ) : (
            grouped.map(([day, dayItems]) => (
              <section key={day}>
                <div className="day">
                  <span>{dayLabel(day, today)}</span>
                  <span className="day__sum">расходы {money(dayExpenses(dayItems))}</span>
                </div>
                <div className="rows">
                  {dayItems.map((tx) => (
                    <Row key={tx.id} tx={tx} me={me} />
                  ))}
                </div>
              </section>
            ))
          )}

          {hasMore && (
            <button className="more" onClick={() => void loadMore()}>
              Показать ещё · осталось {total - items.length}
            </button>
          )}

          <Footer me={me} />
        </div>
      )}
    </div>
  );
}

function Hero({ report }: { report: MonthReport }) {
  const zero = Number(report.total) === 0;
  return (
    <div className="hero">
      <div className="hero__label">Всего за {monthName(report.month).toLowerCase()}</div>
      <div className="hero__value">{money(report.total)}</div>
      {!zero && report.compare && (
        <div className="hero__delta">
          {report.compare.percent >= 0 ? "↑" : "↓"} {Math.abs(report.compare.percent)}% к прошлому
          месяцу{report.compare.partial ? ` · за первые ${report.compare.days} дн.` : ""}
        </div>
      )}
    </div>
  );
}

function SearchSummary({ query, total }: { query: string; total: number }) {
  return (
    <div className="hero">
      <div className="hero__label">Поиск</div>
      <div className="hero__value" style={{ fontSize: 28 }}>
        «{query}»
      </div>
      <div className="hero__delta">
        {total} {plural(total, "запись", "записи", "записей")} за всё время
      </div>
    </div>
  );
}

function Row({ tx, me }: { tx: Tx; me: Me | null }) {
  const isMine = me ? tx.payer_id === me.id : tx.mine;
  const name = isMine ? me?.name ?? "Я" : me?.partner?.name ?? "Партнёр";
  const transfer = tx.kind === "transfer";
  const income = tx.kind === "income";

  return (
    <div className={`row${tx.needs_review ? " row--review" : ""}`}>
      <div className="row__main">
        <div className="row__title">
          {transfer ? "↔ Перевод" : tx.description || "без описания"}
        </div>
        <div className="row__meta">
          {transfer
            ? "не расход"
            : `${tx.needs_review ? "проверить" : tx.category || "без категории"} · ${beneficiaryLabel(
                tx.beneficiary,
              )}`}
        </div>
      </div>
      <div className="row__right">
        <span className={`row__amount${transfer ? " row__amount--muted" : ""}`}>
          {money(tx.amount, income)}
        </span>
        <span className={`who${isMine ? "" : " who--partner"}`} title={name}>
          {initials(name, isMine ? me?.partner?.name : me?.name)}
        </span>
      </div>
    </div>
  );
}

function Footer({ me }: { me: Me | null }) {
  return (
    <div className="foot">
      <span>{me?.name ?? "…"}</span>
      <button
        onClick={async () => {
          await api.logout();
          window.location.reload();
        }}
      >
        Выйти
      </button>
      <button
        onClick={async () => {
          await api.logout(true);
          window.location.reload();
        }}
      >
        Выйти отовсюду
      </button>
    </div>
  );
}

function Empty({ query }: { query: string }) {
  if (query) {
    return <div className="empty">Ничего не нашлось по «{query}»</div>;
  }
  return (
    <div className="empty">
      <p>Трат за этот месяц нет.</p>
      <p>
        Напиши боту <code>600 лимонад</code> — запишется сюда.
      </p>
    </div>
  );
}

function Expired() {
  return (
    <div className="gate">
      <div>
        <h1>Сессия закончилась</h1>
        <p>Напиши боту /вход — он пришлёт новую ссылку.</p>
      </div>
    </div>
  );
}

function Skeleton() {
  return (
    <div aria-hidden>
      <div className="skeleton" style={{ height: 96, margin: "24px 0 16px" }} />
      <div className="skeleton" style={{ height: 24, width: 120, marginBottom: 8 }} />
      <div className="skeleton" style={{ height: 168 }} />
    </div>
  );
}

function groupByDay(items: Tx[]): [string, Tx[]][] {
  const map = new Map<string, Tx[]>();
  for (const tx of items) {
    const list = map.get(tx.day);
    if (list) list.push(tx);
    else map.set(tx.day, [tx]);
  }
  return [...map.entries()];
}

/** Сумма дня — только расходы, как и итог месяца: переводы и доходы не в счёт. */
function dayExpenses(items: Tx[]): string {
  let cents = 0n;
  for (const tx of items) {
    if (tx.kind !== "expense") continue;
    const [whole, frac = ""] = tx.amount.split(".");
    cents += BigInt(whole) * 100n + BigInt(frac.padEnd(2, "0").slice(0, 2));
  }
  const sign = cents < 0n ? "-" : "";
  const abs = cents < 0n ? -cents : cents;
  return `${sign}${abs / 100n}.${String(abs % 100n).padStart(2, "0")}`;
}

function plural(n: number, one: string, few: string, many: string): string {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return one;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return few;
  return many;
}
