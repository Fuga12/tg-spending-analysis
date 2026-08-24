import type { Member } from "./api";

/** Сколько цветов участников есть в палитре (styles.css). */
export const SLOT_COUNT = 10;

/**
 * Раздача цветов участникам.
 *
 * Смотрящий всегда получает первый слот: человек ищет на графике себя, и если
 * его цвет зависит от того, кто ещё есть в группе, искать приходится каждый
 * раз заново. Остальные — по порядку вступления, то есть тоже устойчиво:
 * приход одиннадцатого не перекрашивает всех прежних.
 *
 * Ушедшие участники цвет сохраняют — их траты остались в истории, и менять
 * им цвет задним числом значит переписывать прошлые отчёты.
 */
export function slotsFor(members: Member[], meMemberID: number): Map<number, number> {
  const out = new Map<number, number>();
  out.set(meMemberID, 1);

  let next = 2;
  for (const m of members) {
    if (m.id === meMemberID) continue;
    out.set(m.id, next);
    // Цвета кончились — дальше по кругу. Совпадение двух дальних участников
    // хуже, чем бесцветная марка, но случается только на десятом человеке.
    next = next < SLOT_COUNT ? next + 1 : 2;
  }
  return out;
}

/** Класс марки по ключу строки отчёта: «member:12» или «common». */
export function slotClass(key: string, slots: Map<number, number>): string {
  if (key === "common") return "slot-common";
  const id = Number(key.replace("member:", ""));
  return `slot-${slots.get(id) ?? SLOT_COUNT}`;
}
