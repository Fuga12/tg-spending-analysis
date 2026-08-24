import { useState } from "react";
import { DayPoint, Line, MonthPoint } from "./api";
import { money, plural } from "./format";

/**
 * Графики нарисованы своим SVG по правилам webapp-design.md §6: тонкие марки,
 * зазор 2px цветом фона вместо обводок, выборочные подписи, сплошные
 * волосяные оси. Библиотека ради трёх форм добавила бы 120 КБ и всё равно
 * потребовала бы переопределять каждый стиль.
 */

const num = (v: string) => Number(v);

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
            className={`strip__item${on ? " strip__item--on" : ""}${value === 0 ? " strip__item--empty" : ""}`}
            onClick={() => onPick(p)}
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

const SHORT = ["янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"];

/**
 * Полоса 100%-стека. Подписи под полосой, а не внутри: на 360px сегмент
 * в 15% — это 49px, куда «Партнёру 6 800 ₽» не влезет никогда, а обрезать
 * подписи нельзя (§3.6).
 */
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
  // заголовок с одной строкой «Илья — всё» не сообщает ничего.
  if (lines.length < 2) return null;

  return (
    <section className="block">
      <h2 className="block__title">{title}</h2>
      <div className="stack">
        {lines.map((l) => (
          <div
            key={l.name}
            className={`stack__seg ${slotOf(l)}`}
            style={{ flexGrow: num(l.amount) }}
            title={`${l.name}: ${money(l.amount)}`}
          />
        ))}
      </div>
      <div className={`legend${onPick ? " legend--selectable" : ""}`}>
        {lines.map((l) => (
          <button
			  key={l.name}
			  className={`legend__item${onPick ? " legend__item--action" : ""}${activeKey === l.key ? " legend__item--on" : ""}`}
			  disabled={!onPick}
			  aria-pressed={onPick ? activeKey === l.key : undefined}
			  onClick={() => onPick?.(l)}
			>
            <i className={`legend__dot ${slotOf(l)}`} />
			<span className="legend__name">{l.name}</span>
			<b>{money(l.amount)}</b>
			{onPick && (
			  <span className="legend__check" aria-hidden>
				{activeKey === l.key ? "✓" : ""}
			  </span>
			)}
		  </button>
        ))}
      </div>
    </section>
  );
}

const TOP_CATEGORIES = 8;

/**
 * Категории горизонтальными барами. Все бары одного цвета: это одна серия,
 * и раскрашивать 14 категорий в 14 оттенков — способ сделать их
 * неразличимыми (§3.5).
 */
export function CategoryBars({
  lines,
  activeID,
  onPick,
  deltas,
	scope,
}: {
  lines: Line[];
  activeID: number;
  onPick: (id: number) => void;
  /** Насколько категория изменилась к прошлому месяцу. Это единственный
   *  вопрос, который вообще задают статистике. */
  deltas?: Map<string, number>;
	scope?: { name: string; amount: string };
}) {
  const [expanded, setExpanded] = useState(false);
  if (lines.length === 0) return null;

  const max = Math.max(...lines.map((l) => num(l.amount)), 1);
  // «Без категории» показывается всегда: это не категория, а работа.
  const head = lines.slice(0, TOP_CATEGORIES);
  const tail = lines.slice(TOP_CATEGORIES);
  const orphans = tail.filter((l) => l.id === 0);
  const rest = tail.filter((l) => l.id !== 0);
  const restSum = rest.reduce((sum, l) => sum + num(l.amount), 0);

  return (
    <section className="block">
	  <div className="block__heading">
		<h2 className="block__title">По категориям</h2>
		{scope && <span className="block__scope">{scope.name} · {money(scope.amount)}</span>}
	  </div>
      <div className="bars">
        {[...head, ...orphans, ...(expanded ? rest : [])].map((l) => (
          <button
            key={l.name}
            className={`bar${activeID === l.id && l.id !== 0 ? " bar--on" : ""}`}
            onClick={() => l.id !== 0 && onPick(l.id)}
            disabled={l.id === 0}
          >
            <span className="bar__name">{l.name}</span>
            <span className="bar__row">
              <span className="bar__track">
                <span className="bar__fill" style={{ width: `${(num(l.amount) / max) * 100}%` }} />
              </span>
              <span className="bar__value">{money(l.amount)}</span>
              <span className="bar__delta">{deltaLabel(deltas?.get(l.name))}</span>
            </span>
          </button>
        ))}
      </div>
      {rest.length > 0 && !expanded && (
        <button className="more more--inline" onClick={() => setExpanded(true)}>
          Ещё {rest.length} {plural(rest.length, "категория", "категории", "категорий")} ·{" "}
          {money(String(restSum))}
        </button>
      )}
    </section>
  );
}

/** Разница с прошлым месяцем: «+840 ₽», «−2 200 ₽» или прочерк. */
function deltaLabel(delta: number | undefined) {
  if (delta === undefined || Math.round(delta) === 0) {
    return <span className="delta delta--flat">0 ₽</span>;
  }
  const grew = delta > 0;
  return (
    <span className={`delta ${grew ? "delta--up" : "delta--down"}`}>
      {grew ? "+" : "−"}
      {money(String(Math.abs(Math.round(delta))))}
    </span>
  );
}

/**
 * Дни столбиками. Тапа нет: 31 столбик в 328px — это 10.6px на шаг, палец
 * накрывает четыре сразу. График обзорный, числа есть в списке ниже (§3.7).
 */
export function DayColumns({ days, today }: { days: DayPoint[]; today: string }) {
  if (days.length === 0) return null;

  // Один-два столбика — это не график, а недоразумение.
  if (days.filter((d) => num(d.amount) > 0).length < 3) return null;

  const max = Math.max(...days.map((d) => num(d.amount)), 1);
  const peak = days.reduce((a, b) => (num(a.amount) >= num(b.amount) ? a : b));

  return (
    <section className="block">
      <h2 className="block__title">По дням</h2>
      <div className="days">
        {days.map((d) => {
          const value = num(d.amount);
          const isToday = d.day === today;
          return (
            <span
              key={d.day}
              className={`days__col${isToday ? " days__col--today" : ""}`}
              title={`${Number(d.day.slice(8))}: ${money(d.amount)}`}
            >
              <span
                className="days__fill"
                style={{ height: `${Math.max((value / max) * 100, value > 0 ? 4 : 0)}%` }}
              />
            </span>
          );
        })}
      </div>
      <div className="days__axis">
        {[1, 8, 15, 22, 29].map((d) => (
          <span key={d}>{d}</span>
        ))}
      </div>
      {num(peak.amount) > 0 && (
        <div className="days__note">
          Больше всего {Number(peak.day.slice(8))}-го — {money(peak.amount)}
        </div>
      )}
    </section>
  );
}
