import { Section } from "@telegram-apps/telegram-ui";
import type { DayPoint } from "./api";
import { money, plural } from "./format";

/**
 * Темп трат: сколько в среднем в день и во что это выльется к концу месяца.
 *
 * Итог месяца отвечает на вопрос «сколько уже», но не на «сколько выйдет», а
 * решения принимают по второму. Пятнадцатого числа «45 000 ₽» не значит
 * ничего, пока не сказано, что при таком темпе месяц закроется на 90 000.
 *
 * Считается по прошедшим дням, а не по дням с тратами: дни без трат — это
 * тоже расход, равный нулю, и выкидывать их значит завышать среднее в полтора
 * раза у любого, кто покупает не каждый день.
 */
export function Pace({
  days,
  total,
  year,
  month,
  today,
}: {
  days: DayPoint[];
  total: string;
  year: number;
  month: number;
  today: string;
}) {
  const [ty, tm, td] = today.split("-").map(Number);
  const lastDay = new Date(year, month, 0).getDate();
  const current = ty === year && tm === month;

  // Закрытый месяц прогнозировать нечего — он уже случился.
  const elapsed = current ? Math.min(td, lastDay) : lastDay;
  if (elapsed < 3 || days.length === 0) return null;

  const spent = Number(total);
  if (spent <= 0) return null;

  const perDay = spent / elapsed;
  const forecast = perDay * lastDay;
  const left = lastDay - elapsed;

  return (
    <Section header="Темп">
      <div className="pace">
        <div className="pace__cell">
          <b>{money(String(Math.round(perDay)))}</b>
          <span>в день</span>
        </div>
        {current && left > 0 && (
          <div className="pace__cell">
            <b>{money(String(Math.round(forecast)))}</b>
            <span>выйдет к концу месяца</span>
          </div>
        )}
      </div>
      {current && left > 0 && (
        <p className="pace__note">
          Осталось {left} {plural(left, "день", "дня", "дней")}. Прогноз — если
          тратить так же, как последние {elapsed} {plural(elapsed, "день", "дня", "дней")}.
        </p>
      )}
    </Section>
  );
}
