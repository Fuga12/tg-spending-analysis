// Деньги приходят строками: в JSON число — это float64, а деньги через float
// гонять нельзя.

const NBSP = " ";

/** «1 200 ₽», без копеек, если их нет. */
export function money(value: string, withSign = false): string {
  const negative = value.startsWith("-");
  const [whole, frac = ""] = value.replace("-", "").split(".");

  let out = "";
  for (let i = 0; i < whole.length; i++) {
    if (i > 0 && (whole.length - i) % 3 === 0) out += NBSP;
    out += whole[i];
  }
  const cents = frac.replace(/0+$/, "");
  if (cents) out += "," + cents.padEnd(2, "0");

  const sign = negative ? "−" : withSign ? "+" : "";
  return `${sign}${out}${NBSP}₽`;
}

const MONTHS = [
  "Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
  "Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь",
];

const MONTHS_OF = [
  "января", "февраля", "марта", "апреля", "мая", "июня",
  "июля", "августа", "сентября", "октября", "ноября", "декабря",
];

const WEEKDAYS = ["вс", "пн", "вт", "ср", "чт", "пт", "сб"];

export const monthName = (month: number) => MONTHS[month - 1];

/** «Сегодня», «Вчера» или «12 июля · пт». День приходит как YYYY-MM-DD. */
export function dayLabel(day: string, today: string): string {
  if (day === today) return "Сегодня";

  const [y, m, d] = day.split("-").map(Number);
  const [ty, tm, td] = today.split("-").map(Number);
  const diff = Math.round(
    (Date.UTC(ty, tm - 1, td) - Date.UTC(y, m - 1, d)) / 86400000,
  );
  if (diff === 1) return "Вчера";
  if (diff === 2) return "Позавчера";

  const weekday = WEEKDAYS[new Date(Date.UTC(y, m - 1, d)).getUTCDay()];
  return `${d} ${MONTHS_OF[m - 1]} · ${weekday}`;
}

/** Дата в таймзоне бота приходит с сервера; «сегодня» берём из неё же. */
export function todayFrom(list: { day: string }[]): string {
  const now = new Date();
  const local = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(
    now.getDate(),
  ).padStart(2, "0")}`;
  return list.length && list[0].day > local ? list[0].day : local;
}

/**
 * Инициалы для кружка участника.
 *
 * Две буквы, а не одна: в группе из десяти человек одна буква совпадает
 * почти наверняка, и кружки становятся неразличимы. Цвет слота помогает, но
 * на него одного полагаться нельзя — дальтонизм никуда не делся.
 */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length > 1) {
    return ([...parts[0]][0] + [...parts[1]][0]).toUpperCase();
  }
  return [...parts[0]].slice(0, 2).join("").toUpperCase();
}

/** Русское склонение по числу: 1 запись, 2 записи, 5 записей. */
export function plural(n: number, one: string, few: string, many: string): string {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return one;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return few;
  return many;
}
