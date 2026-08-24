import { useCallback, useEffect, useMemo, useState } from "react";
import { Button, Cell, LargeTitle, List, Section, Tabbar } from "@telegram-apps/telegram-ui";
import {
  ApiError,
  api,
  tg,
  type DayPoint,
  type MonthPoint,
  type MonthReport,
  type State,
  type Tx,
  type TxFilter,
} from "./api";
import { Categories } from "./Categories";
import { CategoryBars, DayColumns, MonthStrip, SpendingTrend, StackedBar } from "./Charts";
import { GroupScreen, NoGroupScreen } from "./GroupScreen";
import { money, monthName, plural } from "./format";
import { slotClass, slotsFor } from "./slots";
import { TxEdit } from "./TxEdit";
import { TxList } from "./TxList";
import { Empty, ErrorBar, Loading } from "./ui";

type Tab = "month" | "list" | "group" | "cats";

const TABS: [Tab, string][] = [
  ["month", "Месяц"],
  ["list", "Траты"],
  ["group", "Группа"],
  ["cats", "Категории"],
];

export default function App() {
  const [state, setState] = useState<State | null>(null);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<Tab>("month");

  const now = new Date();
  const [period, setPeriod] = useState({ year: now.getFullYear(), month: now.getMonth() + 1 });

  const [report, setReport] = useState<MonthReport | null>(null);
  const [days, setDays] = useState<DayPoint[]>([]);
  // Дни прошлого месяца нужны накопительной кривой: без второй линии она
  // показывает сумму, но не отвечает на вопрос «это много или как обычно».
  const [prevDays, setPrevDays] = useState<DayPoint[]>([]);
  const [months, setMonths] = useState<MonthPoint[]>([]);

  const [filter, setFilter] = useState<TxFilter>({});
  const [items, setItems] = useState<Tx[]>([]);
  const [total, setTotal] = useState(0);
  const [editing, setEditing] = useState<Tx | null>(null);

  useEffect(() => {
    tg?.ready();
    tg?.expand();
  }, []);

  const loadState = useCallback(async () => {
    try {
      setState(await api.state());
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Не удалось связаться с сервером.");
    }
  }, []);

  useEffect(() => {
    void loadState();
  }, [loadState]);

  const inGroup = Boolean(state?.group);

  // Отчёт и полоса месяцев грузятся вместе: полоса — это навигация, и без
  // неё месяц не переключить.
  useEffect(() => {
    if (!inGroup) return;
    const prev =
      period.month === 1
        ? { year: period.year - 1, month: 12 }
        : { year: period.year, month: period.month - 1 };

    Promise.all([
      api.month(period.year, period.month),
      api.days(period.year, period.month),
      api.days(prev.year, prev.month),
      api.months(),
    ])
      .then(([m, d, pd, ms]) => {
        setReport(m);
        setDays(d);
        setPrevDays(pd);
        setMonths(ms);
      })
      .catch((e) => setError(e instanceof ApiError ? e.message : "Не смог посчитать отчёт."));
  }, [inGroup, period.year, period.month]);

  const reload = useCallback(
    async (f: TxFilter) => {
      if (!inGroup) return;
      try {
        const res = await api.transactions({ ...f, ...monthBounds(period) });
        setItems(res.items);
        setTotal(res.total);
      } catch (e) {
        setError(e instanceof ApiError ? e.message : "Не смог загрузить траты.");
      }
    },
    [inGroup, period],
  );

  useEffect(() => {
    void reload(filter);
  }, [reload, filter]);

  const slots = useMemo(
    () => slotsFor(state?.members ?? [], state?.group?.member_id ?? 0),
    [state?.members, state?.group?.member_id],
  );

  if (!state) {
    return (
      <List>{error ? <ErrorBar text={error} onClose={() => setError("")} /> : <Loading />}</List>
    );
  }

  if (!state.group) {
    return (
      <List>
        <NoGroupScreen state={state} onChanged={loadState} />
      </List>
    );
  }

  // Тап по строке отчёта фильтрует список — это и есть ответ на вопрос
  // «из чего сложилась эта сумма».
  const drillTo = (f: TxFilter) => {
    setFilter(f);
    setTab("list");
  };

  return (
    <>
      {/* Отступ снизу под таббар: иначе последняя строка списка уезжает
          под него и до неё не дотянуться. */}
      <List className="app">
        {error && <ErrorBar text={error} onClose={() => setError("")} />}

        {tab === "month" && report && (
          <>
            <MonthStrip points={months} active={period} onPick={setPeriod} />

            <Section>
              <div className="total">
                <LargeTitle weight="1">{money(report.total)}</LargeTitle>
                <span className="total__month">
                  {monthName(period.month)} {period.year}
                </span>
                <Comparison report={report} />
              </div>
            </Section>

            {report.review > 0 && (
              <Section footer="Категорию этим записям выбрал не человек, а бот — вслепую.">
                <Cell onClick={() => drillTo({ pending: true })}>
                  {report.review} {plural(report.review, "запись", "записи", "записей")} стоит проверить
                </Cell>
              </Section>
            )}

            <SpendingTrend
              current={days}
              previous={prevDays}
              year={period.year}
              month={period.month}
              today={todayISO()}
            />

            <DayColumns days={days} today={todayISO()} />

            <StackedBar
              title="Кто платил"
              lines={report.payers}
              slotOf={(l) => slotClass(l.key, slots)}
              activeKey={filter.payer ? `member:${filter.payer}` : undefined}
              onPick={(l) => drillTo({ payer: l.id })}
            />

            <StackedBar
              title="На кого ушло"
              lines={report.beneficiaries}
              slotOf={(l) => slotClass(l.key, slots)}
              activeKey={
                filter.recipient === "common"
                  ? "common"
                  : filter.recipient
                    ? `member:${filter.recipient}`
                    : undefined
              }
              onPick={(l) => drillTo({ recipient: l.key === "common" ? "common" : l.id })}
            />

            <CategoryBars
              lines={report.categories}
              activeID={filter.category ?? 0}
              onPick={(id) => drillTo({ category: id })}
            />
          </>
        )}

        {tab === "list" && (
          <>
            <Section
              header={`${monthName(period.month)} ${period.year}`}
              footer={`${total} ${plural(total, "запись", "записи", "записей")}`}
            >
              {hasFilter(filter) && (
                <div className="sheet-actions">
                  <Button size="s" mode="bezeled" stretched onClick={() => setFilter({})}>
                    Показать все за месяц
                  </Button>
                </div>
              )}
            </Section>

            {items.length === 0 ? (
              <Empty title="Пусто" hint="Напиши боту тратой — она появится здесь." />
            ) : (
              <TxList
                items={items}
                members={state.members}
                categories={state.categories}
                slots={slots}
                onPick={setEditing}
              />
            )}
          </>
        )}

        {tab === "group" && <GroupScreen state={state} slots={slots} onChanged={loadState} />}

        {tab === "cats" && (
          <Categories categories={state.categories} members={state.members} onChanged={loadState} />
        )}
      </List>

      {/* Обёртка нужна, чтобы дотянуться до высоты панели: классы библиотеки
          захешированы, и целиться в них по имени нельзя. */}
      <div className="tabbar">
        <Tabbar>
          {TABS.map(([id, label]) => (
            <Tabbar.Item key={id} text={label} selected={tab === id} onClick={() => setTab(id)} />
          ))}
        </Tabbar>
      </div>

      {editing && (
        <TxEdit
          tx={editing}
          members={state.members}
          categories={state.categories}
          slots={slots}
          onClose={() => setEditing(null)}
          onDone={(next) => {
            setEditing(null);
            setItems((prev) =>
              next
                ? prev.map((t) => (t.id === next.id ? next : t))
                : prev.filter((t) => t.id !== editing.id),
            );
            // Отчёт пересчитывается: правка меняет и суммы, и раскладку.
            setPeriod({ ...period });
          }}
        />
      )}
    </>
  );
}

/**
 * Сравнение с прошлым месяцем.
 *
 * Сравнивается сопоставимый отрезок: незакрытый месяц — с тем же числом дней
 * прошлого. Иначе 5 июля всегда «на 80% меньше», и это не информация. Первые
 * дни месяца сервер не сравнивает вовсе — оттуда и null.
 */
function Comparison({ report }: { report: MonthReport }) {
  if (!report.compare) return null;

  const prev = Number(report.compare.previous);
  const cur = Number(report.compare.current);
  if (prev <= 0) return null;

  const delta = Math.round(((cur - prev) / prev) * 100);
  if (delta === 0) return <span className="compare">столько же, сколько в прошлом месяце</span>;

  return (
    <span className={`compare${delta > 0 ? " compare--up" : " compare--down"}`}>
      {delta > 0 ? "+" : "−"}
      {Math.abs(delta)}% к прошлому месяцу
      {report.compare.partial &&
        ` за те же ${report.compare.days} ${plural(report.compare.days, "день", "дня", "дней")}`}
    </span>
  );
}

/** Сегодня по часам устройства: сервер отдаёт моменты времени, а «какой это
 *  день» человек читает по своим. */
function todayISO(): string {
  const d = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function monthBounds({ year, month }: { year: number; month: number }) {
  const pad = (n: number) => String(n).padStart(2, "0");
  const last = new Date(year, month, 0).getDate();
  return { from: `${year}-${pad(month)}-01`, to: `${year}-${pad(month)}-${pad(last)}` };
}

function hasFilter(f: TxFilter): boolean {
  return Boolean(f.payer || f.recipient || f.category || f.pending || f.kind || f.q);
}
