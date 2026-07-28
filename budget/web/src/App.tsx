import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, Category, DayPoint, Line, Me, MonthPoint, MonthReport, Tx, Unauthorized } from "./api";
import Sheet from "./Sheet";
import { CategoryBars, DayColumns, Sparkline, StackedBar } from "./Charts";
import { beneficiaryLabel, dayLabel, initials, money, monthName, todayFrom } from "./format";

const PAGE = 200;

const MONTHS_IN = [
  "январе", "феврале", "марте", "апреле", "мае", "июне",
  "июле", "августе", "сентябре", "октябре", "ноябре", "декабре",
];

const monthOf = (month: number) => MONTHS_IN[month - 1];

/** Цвет закреплён за человеком: смотрящий — первый слот, партнёр — второй,
 *  общие корзины — третий. Не по порядку в базе: иначе у двоих будут разные
 *  цвета у одних и тех же людей (webapp-design.md §3.6). */
function slotOf(line: Line, me: Me | null): number {
  if (line.id === 0) return 3;
  if (me && line.id === me.id) return 1;
  return 2;
}

/** Дата новой записи: в открытом прошлом месяце — его первое число, иначе
 *  сегодня. Иначе запись уезжает в текущий месяц и на экране не появляется. */
function defaultDayFor(route: Route, today: string): string {
  const [y, m] = today.split("-").map(Number);
  if (route.year === y && route.month === m) return today;
  return `${route.year}-${String(route.month).padStart(2, "0")}-01`;
}

/** Одни и те же фильтры для первой страницы и для догрузки. */
function listParams(route: Route) {
  if (route.query) return { q: route.query };
  return {
    year: route.year,
    month: route.month,
    pending: route.pending ? 1 : undefined,
    category: route.category || undefined,
  };
}

type Route = { year: number; month: number; query: string; pending: boolean; category: number };

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
    category: Number(params.get("cat") ?? 0) || 0,
  };
}

function hashFor(r: Route): string {
  const params = new URLSearchParams();
  if (r.query) params.set("q", r.query);
  if (r.pending) params.set("pending", "1");
  if (r.category) params.set("cat", String(r.category));
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
  const [categories, setCategories] = useState<Category[]>([]);
  const [editing, setEditing] = useState<Tx | null>(null);
  const [creating, setCreating] = useState(false);
  const [toast, setToast] = useState<{ text: string; undo?: () => void } | null>(null);
  const [days, setDays] = useState<DayPoint[]>([]);
  const [months, setMonths] = useState<MonthPoint[]>([]);

  // Номер запроса: ответы по параллельным соединениям приходят не по
  // порядку, и без этого три быстрых нажатия «‹» оставляют на экране июнь
  // под заголовком «Июль».
  const request = useRef(0);
  const [loadingMore, setLoadingMore] = useState(false);

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
    api.categories().then(setCategories).catch(() => {});
    api.months().then(setMonths).catch(() => {});
  }, []);

  // Тост живёт шесть секунд: столько нужно, чтобы передумать удалять.
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(null), 6000);
    return () => clearTimeout(timer);
  }, [toast]);

  function handleAuthError(err: unknown) {
    if (err instanceof Unauthorized) setExpired(true);
    else setError(err instanceof Error ? err.message : "что-то сломалось");
  }

  const load = useCallback(async () => {
    // При смене месяца прошлый экран приглушается, а не мигает скелетом.
    if (report) setRefreshing(true);
    setError(null);

    const id = ++request.current;
    try {
      const [monthData, page, dayData] = await Promise.all([
        route.query ? Promise.resolve(null) : api.month(route.year, route.month),
        api.transactions({ ...listParams(route), limit: PAGE }),
        route.query ? Promise.resolve([]) : api.daily(route.year, route.month),
      ]);

      if (id !== request.current) return; // ответ устарел, пришёл другой месяц

      if (monthData) setReport(monthData);
      setDays(dayData);
      setItems(page.items);
      setTotal(page.total);
      setHasMore(page.has_more);
    } catch (err) {
      if (id === request.current) handleAuthError(err);
    } finally {
      if (id === request.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [route.year, route.month, route.query, route.pending, route.category]); // eslint-disable-line

  useEffect(() => {
    void load();
  }, [load]);

  async function loadMore() {
    if (loadingMore) return; // второй тап по «Показать ещё» приклеил бы ту же страницу
    setLoadingMore(true);
    try {
      const page = await api.transactions({
        ...listParams(route),
        limit: PAGE,
        offset: items.length,
      });
      // Пока листали, бот мог записать новую трату: страницы сдвигаются,
      // и по offset приезжают уже показанные записи.
      setItems((prev) => {
        const seen = new Set(prev.map((tx) => tx.id));
        return [...prev, ...page.items.filter((tx) => !seen.has(tx.id))];
      });
      setHasMore(page.has_more);
      setTotal(page.total);
    } catch (err) {
      handleAuthError(err);
    } finally {
      setLoadingMore(false);
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
            <button
              className="iconbtn"
              aria-label="Закрыть поиск"
              onClick={() => {
                setSearching(false);
                go({ ...route, query: "" });
              }}
            >
              ✕
            </button>
            <div className="search" style={{ flex: 1, paddingBottom: 0 }}>
              <input
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
        <div className="notice notice--error" role="alert">
          <span aria-hidden>⚠</span>
          <span style={{ flex: 1 }}>{error}</span>
          <button className="notice__action" onClick={() => void load()}>
            Повторить
          </button>
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

          {!route.query && report && Number(report.total) > 0 && (
            <>
              <Sparkline
                points={months}
                onPick={(p) => go({ ...route, query: "", year: p.year, month: p.month })}
              />
              <StackedBar title="Кто платил" lines={report.payers} slotOf={(l) => slotOf(l, me)} />
              <StackedBar title="На кого ушло" lines={report.beneficiaries} slotOf={(l) => slotOf(l, me)} />
              <CategoryBars
                lines={report.categories}
                activeID={route.category}
                onPick={(id) => go({ ...route, category: route.category === id ? 0 : id }, true)}
              />
              <DayColumns days={days} today={today} />
            </>
          )}

          {route.category > 0 && (
            <div className="foot" style={{ paddingTop: 8 }}>
              {report?.categories.find((c) => c.id === route.category)?.name ?? "Категория"} · {total}
              <button onClick={() => go({ ...route, category: 0 }, true)}>снять фильтр</button>
            </div>
          )}

          {route.pending && (
            <div className="foot" style={{ paddingTop: 8 }}>
              На проверку · {total}
              <button onClick={() => go({ ...route, pending: false }, true)}>снять фильтр</button>
            </div>
          )}

          {items.length === 0 ? (
            <Empty query={route.query} month={route.month} />
          ) : (
            grouped.map(([day, dayItems]) => (
              <section key={day}>
                <div className="day">
                  <span>{dayLabel(day, today)}</span>
                  <span className="day__sum">расходы {money(dayExpenses(dayItems))}</span>
                </div>
                <div className="rows">
                  {dayItems.map((tx) => (
                    <Row key={tx.id} tx={tx} me={me} onOpen={() => setEditing(tx)} />
                  ))}
                </div>
              </section>
            ))
          )}

          {hasMore && (
            <button className="more" onClick={() => void loadMore()} disabled={loadingMore}>
              {loadingMore ? "Гружу…" : `Показать ещё · осталось ${Math.max(total - items.length, 0)}`}
            </button>
          )}

          <Footer me={me} />
        </div>
      )}

      {!loading && (
        <button className="fab" onClick={() => setCreating(true)} aria-label="Добавить запись">
          +
        </button>
      )}

      {(editing || creating) && (
        <Sheet
          tx={editing}
          categories={categories}
          today={today}
          partnerName={me?.partner?.name}
          defaultDay={editing ? editing.day : defaultDayFor(route, today)}
          onClose={() => {
            setEditing(null);
            setCreating(false);
          }}
          onSaved={() => void load()}
          onDeleted={(tx) => {
            setItems((prev) => prev.filter((item) => item.id !== tx.id));
            void load();
            setToast({
              text: "Удалено",
              undo: async () => {
                try {
                  await api.restore(tx.id);
                } catch (err) {
                  handleAuthError(err);
                } finally {
                  setToast(null);
                  void load();
                }
              },
            });
          }}
        />
      )}

      {toast && (
        <div className="toast" role="status">
          <span style={{ flex: 1 }}>{toast.text}</span>
          {toast.undo && (
            <button className="toast__action" onClick={() => void toast.undo!()}>
              Вернуть
            </button>
          )}
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
          {deltaText(report)}
          {report.compare.partial ? ` · за первые ${report.compare.days} дн.` : ""}
        </div>
      )}
    </div>
  );
}

/** Процент врёт на малой базе — тогда показываем разницу в рублях. */
function deltaText(report: MonthReport): string {
  const c = report.compare!;
  const prevMonth = monthOf(report.month === 1 ? 12 : report.month - 1);
  if (c.has_percent) {
    return `${c.percent >= 0 ? "↑" : "↓"} ${Math.abs(c.percent)}% к ${prevMonth}`;
  }
  const grew = !c.difference.startsWith("-");
  return `${grew ? "↑" : "↓"} ${money(c.difference.replace("-", ""))} к ${prevMonth}`;
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

function Row({ tx, me, onOpen }: { tx: Tx; me: Me | null; onOpen: () => void }) {
  const isMine = me ? tx.payer_id === me.id : tx.mine;
  const name = isMine ? me?.name ?? "Я" : me?.partner?.name ?? "Партнёр";
  const transfer = tx.kind === "transfer";
  const income = tx.kind === "income";

  return (
    <button className={`row${tx.needs_review ? " row--review" : ""}`} onClick={onOpen}>
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
    </button>
  );
}

async function leave(everywhere: boolean) {
  try {
    await api.logout(everywhere);
  } finally {
    // Даже если запрос не дошёл, перезагрузка покажет экран входа.
    window.location.reload();
  }
}

function Footer({ me }: { me: Me | null }) {
  return (
    <div className="foot">
      <span>{me?.name ?? "…"}</span>
      <button onClick={() => void leave(false)}>Выйти</button>
      <button onClick={() => void leave(true)}>Выйти отовсюду</button>
    </div>
  );
}

function Empty({ query, month }: { query: string; month: number }) {
  if (query) {
    return <div className="empty">Ничего не нашлось по «{query}»</div>;
  }
  return (
    <div className="empty">
      <p>В {monthOf(month)} трат нет.</p>
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
