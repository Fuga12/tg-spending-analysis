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

/**
 * Дни столбиками. Тапа нет: 31 столбик на ширину телефона — это 10 пикселей
 * на шаг, палец накрывает четыре сразу. График обзорный, числа есть в списке.
 */
export function DayColumns({ days, today }: { days: DayPoint[]; today: string }) {
  // Один-два столбика — это не график, а недоразумение.
  if (days.filter((d) => num(d.amount) > 0).length < 3) return null;

  const max = Math.max(...days.map((d) => num(d.amount)), 1);
  const peak = days.reduce((a, b) => (num(a.amount) >= num(b.amount) ? a : b));

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
        {days.map((d) => {
          const value = num(d.amount);
          return (
            <span
              key={d.day}
              className={`days__col${d.day === today ? " days__col--today" : ""}`}
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
    </Section>
  );
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
}: {
  lines: Line[];
  activeID: number;
  onPick: (id: number) => void;
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
