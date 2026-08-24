import { useState } from "react";
import { Section } from "@telegram-apps/telegram-ui";
import type { DayPoint, Line, MonthPoint } from "./api";
import { money, plural } from "./format";

/**
 * Графики нарисованы своей вёрсткой: тонкие марки, зазор цветом фона вместо
 * обводок, выборочные подписи. Библиотека графиков ради трёх форм добавила бы
 * сотню килобайт и всё равно потребовала бы переопределять каждый стиль,
 * чтобы попасть в оформление Telegram.
 *
 * Цвета берутся у клиента через переменные --tgui--*, поэтому графики меняют
 * тему вместе с ним. Свои цвета остались только у участников: они связывают
 * сегмент полосы с кружком в списке, и без этой связи легенда ничего
 * не объясняет.
 */

const num = (v: string) => Number(v);

const SHORT = ["янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"];

/**
 * Полоса месяцев: она же навигация, она же график тренда. Линия с точкой
 * решала только вторую задачу и занимала столько же места.
 */
export function MonthStrip({
  points,
  active,
  onPick,
}: {
  points: MonthPoint[];
  active: { year: number; month: number };
  onPick: (p: { year: number; month: number }) => void;
}) {
  if (points.length === 0) return null;
  const max = Math.max(...points.map((p) => num(p.amount)), 1);

  return (
    <div className="strip">
      {points.map((p) => {
        const on = p.year === active.year && p.month === active.month;
        const value = num(p.amount);
        return (
          <button
            key={`${p.year}-${p.month}`}
            className={`strip__item${on ? " strip__item--on" : ""}`}
            onClick={() => onPick({ year: p.year, month: p.month })}
            title={value === 0 ? "трат не было" : money(p.amount)}
          >
            {/* Пустой месяц — черта у основания, а не огрызок бара: иначе
                шкала выглядит сломанной, а не пустой. */}
            <span
              className="strip__bar"
              style={{ height: value === 0 ? "2px" : `${Math.max((value / max) * 100, 8)}%` }}
            />
            <span className="strip__label">{SHORT[p.month - 1]}</span>
          </button>
        );
      })}
    </div>
  );
}

/** Сколько категорий показывать цветом. Дальше глаз не различает, и
 *  остальное честнее свести в одну серую долю, чем красить в седьмой оттенок. */
const TOP_DAY_CATEGORIES = 5;

/**
 * Расход по дням, разложенный по категориям.
 *
 * Столбик отвечает «когда», цвет — «на что». Порознь это два графика, из
 * которых первый не объясняет всплески, а второй не показывает, когда они
 * случились.
 *
 * Категорий цветом ровно пять: на ширине телефона день — это десять
 * пикселей, и делить их на четырнадцать долей значит рисовать шум. Остальное
 * уходит в серое «прочее» — доля, а не выдуманный оттенок.
 */
export function DayCategories({
  days,
  categories,
  today,
  onPick,
}: {
  days: DayPoint[];
  categories: Line[];
  today: string;
  onPick?: (categoryID: number) => void;
}) {
  if (days.every((d) => num(d.amount) <= 0)) return null;

  // Порядок берётся из месячного отчёта: он уже отсортирован по убыванию,
  // и цвет категории не скачет от дня к дню.
  const top = categories.filter((c) => c.id !== 0).slice(0, TOP_DAY_CATEGORIES);
  const colorOf = new Map(top.map((c, i) => [String(c.id), `cat-${i + 1}`]));

  const max = Math.max(...days.map((d) => num(d.amount)), 1);
  const peak = days.reduce((a, b) => (num(a.amount) >= num(b.amount) ? a : b));
  const hasRest = categories.some((c) => !colorOf.has(String(c.id)));

  return (
    <Section
      header="По дням"
      footer={
        num(peak.amount) > 0
          ? `Больше всего ${Number(peak.day.slice(8))}-го — ${money(peak.amount)}`
          : undefined
      }
    >
      <div className="days">
        {days.map((d) => (
          <span
            key={d.day}
            className={`days__col${d.day === today ? " days__col--today" : ""}`}
            title={`${Number(d.day.slice(8))}: ${money(d.amount)}`}
          >
            {/* Столбик собирается снизу вверх долями категорий. Высота всего
                столбика — доля от самого дорогого дня, а не от месяца:
                иначе в месяце с одной крупной тратой все остальные дни
                вырождаются в полоску. */}
            <span
              className="days__stack"
              style={{ height: `${Math.max((num(d.amount) / max) * 100, num(d.amount) > 0 ? 4 : 0)}%` }}
            >
              {segmentsOf(d, colorOf).map((seg) => (
                <span
                  key={seg.key}
                  className={`days__part ${seg.cls}`}
                  style={{ flexGrow: seg.value }}
                />
              ))}
            </span>
          </span>
        ))}
      </div>
      <div className="days__axis">
        {[1, 8, 15, 22, 29].map((d) => (
          <span key={d}>{d}</span>
        ))}
      </div>

      <div className="legend">
        {top.map((c, i) => (
          <button
            key={c.id}
            className="legend__item"
            disabled={!onPick}
            onClick={() => onPick?.(c.id)}
          >
            <i className={`legend__dot cat-${i + 1}`} />
            <span className="legend__name">{c.name}</span>
            <b className="amount">{money(c.amount)}</b>
          </button>
        ))}
        {hasRest && (
          <span className="legend__item legend__item--flat">
            <i className="legend__dot cat-rest" />
            <span className="legend__name">прочее</span>
          </span>
        )}
      </div>
    </Section>
  );
}

/** Доли одного дня, в том же порядке, что и легенда: иначе цвета в соседних
 *  столбиках стоят на разной высоте и полосы читаются как рябь. */
function segmentsOf(day: DayPoint, colorOf: Map<string, string>) {
  const out: { key: string; cls: string; value: number }[] = [];
  let rest = 0;

  for (const [id, amount] of Object.entries(day.by)) {
    const value = num(amount);
    if (value <= 0) continue;
    const cls = colorOf.get(id);
    if (cls) out.push({ key: id, cls, value });
    else rest += value;
  }
  out.sort((a, b) => a.cls.localeCompare(b.cls));
  if (rest > 0) out.push({ key: "rest", cls: "cat-rest", value: rest });
  return out;
}

/** Полоса 100%-стека с подписями под ней. */
export function StackedBar({
  title,
  lines,
  slotOf,
  onPick,
  activeKey,
}: {
  title: string;
  lines: Line[];
  slotOf: (line: Line) => string;
  onPick?: (line: Line) => void;
  activeKey?: string;
}) {
  const total = lines.reduce((sum, l) => sum + num(l.amount), 0);
  if (total <= 0) return null;

  // Полоса из одного сегмента — это не доля, а закрашенный прямоугольник:
  // блок с единственной строкой «Илья — всё» не сообщает ничего.
  if (lines.length < 2) return null;

  return (
    <Section header={title}>
      <div className="stack">
        {lines.map((l) => (
          <div
            key={l.key || l.name}
            className={`stack__seg ${slotOf(l)}`}
            style={{ flexGrow: num(l.amount) }}
            title={`${l.name}: ${money(l.amount)}`}
          />
        ))}
      </div>
      <div className="legend">
        {lines.map((l) => (
          <button
            key={l.key || l.name}
            className={`legend__item${activeKey === l.key ? " legend__item--on" : ""}`}
            disabled={!onPick}
            aria-pressed={onPick ? activeKey === l.key : undefined}
            onClick={() => onPick?.(l)}
          >
            <i className={`legend__dot ${slotOf(l)}`} />
            <span className="legend__name">{l.name}</span>
            <b className="amount">{money(l.amount)}</b>
            {onPick && <span className="legend__check">{activeKey === l.key ? "✓" : ""}</span>}
          </button>
        ))}
      </div>
    </Section>
  );
}

const TOP_CATEGORIES = 8;

/**
 * Категории горизонтальными барами. Все бары одного цвета: это одна серия,
 * и раскрасить четырнадцать категорий в четырнадцать оттенков — способ
 * сделать их неразличимыми.
 */
export function CategoryBars({
  lines,
  activeID,
  onPick,
  deltas,
}: {
  lines: Line[];
  activeID: number;
  onPick: (id: number) => void;
  /** Насколько категория изменилась к прошлому месяцу. Это единственный
   *  вопрос, который вообще задают статистике: что подорожало. */
  deltas?: Map<string, number>;
}) {
  const [expanded, setExpanded] = useState(false);
  if (lines.length === 0) return null;

  const max = Math.max(...lines.map((l) => num(l.amount)), 1);
  const head = lines.slice(0, TOP_CATEGORIES);
  const tail = lines.slice(TOP_CATEGORIES);
  // «Без категории» показывается всегда: это не категория, а работа.
  const orphans = tail.filter((l) => l.id === 0);
  const rest = tail.filter((l) => l.id !== 0);
  const restSum = rest.reduce((sum, l) => sum + num(l.amount), 0);

  return (
    <Section header="По категориям">
      {[...head, ...orphans, ...(expanded ? rest : [])].map((l) => (
        <button
          key={l.name}
          className={`bar${activeID === l.id && l.id !== 0 ? " bar--on" : ""}`}
          onClick={() => l.id !== 0 && onPick(l.id)}
          disabled={l.id === 0}
        >
          <span>{l.name}</span>
          <span className="bar__row">
            <span className="bar__track">
              <span className="bar__fill" style={{ width: `${(num(l.amount) / max) * 100}%` }} />
            </span>
            <span className="bar__value">{money(l.amount)}</span>
            {deltas && <span className="bar__delta">{deltaLabel(deltas.get(l.name))}</span>}
          </span>
        </button>
      ))}
      {rest.length > 0 && !expanded && (
        <button className="more" onClick={() => setExpanded(true)}>
          Ещё {rest.length} {plural(rest.length, "категория", "категории", "категорий")} ·{" "}
          {money(String(restSum))}
        </button>
      )}
    </Section>
  );
}


/** Разница с прошлым месяцем: «+840 ₽», «−2 200 ₽» или прочерк. */
function deltaLabel(delta: number | undefined) {
  if (delta === undefined || Math.round(delta) === 0) {
    return <span className="delta delta--flat">—</span>;
  }
  const grew = delta > 0;
  return (
    <span className={`delta ${grew ? "delta--up" : "delta--down"}`}>
      {grew ? "+" : "−"}
      {money(String(Math.abs(Math.round(delta))))}
    </span>
  );
}
