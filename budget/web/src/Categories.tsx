import { useState } from "react";
import { Button, Cell, Chip, Input, Section } from "@telegram-apps/telegram-ui";
import { ApiError, api, type Category, type DefaultTo, type Member } from "./api";
import { ErrorBar, Sheet } from "./ui";

/**
 * Категории группы.
 *
 * Подсказка — не украшение: она уезжает в промпт и в JSON-схему, и это
 * единственный способ научить модель различать то, что путает именно эту
 * группу. «Алкоголь — пиво, вино» перестаёт уходить в Продукты.
 */
export function Categories({
  categories,
  members,
  onChanged,
}: {
  categories: Category[];
  members: Member[];
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState<Category | "new" | null>(null);
  const byID = new Map(members.map((m) => [m.id, m]));

  return (
    <>
      <Section
        header="Категории"
        footer="Бот раскладывает траты по этим категориям. Открой любую, чтобы объяснить ему, что к ней относится."
      >
        {categories.map((c) => (
          <Cell
            key={c.id}
            multiline
            subtitle={c.hint || undefined}
            description={describeDefault(c.default_to, byID)}
            onClick={() => setEditing(c)}
          >
            {c.name}
          </Cell>
        ))}
      </Section>

      <Section>
        <div className="sheet-actions">
          <Button size="l" stretched onClick={() => setEditing("new")}>
            Добавить категорию
          </Button>
        </div>
      </Section>

      {editing && (
        <CategorySheet
          category={editing === "new" ? null : editing}
          members={members}
          onClose={() => setEditing(null)}
          onDone={() => {
            setEditing(null);
            onChanged();
          }}
        />
      )}
    </>
  );
}

/** Подпись под названием категории в списке. У «на того, кто заплатил»
 *  подписи нет: это умолчание, и писать его у каждой строки значит
 *  утопить в нём те две-три, которые настроены иначе. */
function describeDefault(d: DefaultTo, byID: Map<number, Member>): string | undefined {
  if (d === "common") return "записывается на всю группу";
  if (d === null) return undefined;
  return `записывается на: ${byID.get(d)?.name ?? "кого-то"}`;
}

function CategorySheet({
  category,
  members,
  onClose,
  onDone,
}: {
  category: Category | null;
  members: Member[];
  onClose: () => void;
  onDone: () => void;
}) {
  const [name, setName] = useState(category?.name ?? "");
  const [hint, setHint] = useState(category?.hint ?? "");
  const [defaultTo, setDefaultTo] = useState<DefaultTo>(category?.default_to ?? null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const save = async () => {
    if (!name.trim()) {
      setError("У категории должно быть название.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      if (category) await api.updateCategory(category.id, name, hint, defaultTo);
      else await api.createCategory(name, hint, defaultTo);
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Не смог сохранить.");
      setBusy(false);
    }
  };

  return (
    <Sheet title={category ? "Категория" : "Новая категория"} onClose={onClose}>
      {error && <ErrorBar text={error} onClose={() => setError("")} />}

      <Section>
        <Input header="Название" value={name} onChange={(e) => setName(e.target.value)} />
      </Section>

      <Section
        header="Что сюда относится"
        footer="Список слов через запятую. Он уходит боту вместе с сообщением: так он поймёт, что «шаурма» — это еда, а не хозтовары. Можно оставить пустым."
      >
        <Input
          placeholder="пиво, вино, коктейли"
          value={hint}
          onChange={(e) => setHint(e.target.value)}
        />
      </Section>

      <Section
        header="На кого записывать такие траты"
        footer="Так бот поступит, когда в сообщении не сказано, кому трата. «На всю группу» — для общего: продукты, дом, коммуналка. Имя — для того, что всегда покупают одному и тому же, кто бы ни платил."
      >
        <div className="chips">
          <Chip mode={defaultTo === null ? "elevated" : "outline"} onClick={() => setDefaultTo(null)}>
            На того, кто заплатил
          </Chip>
          <Chip
            mode={defaultTo === "common" ? "elevated" : "outline"}
            onClick={() => setDefaultTo("common")}
          >
            На всю группу
          </Chip>
          {members
            .filter((m) => !m.left)
            .map((m) => (
              <Chip
                key={m.id}
                mode={defaultTo === m.id ? "elevated" : "outline"}
                onClick={() => setDefaultTo(m.id)}
              >
                {m.name}
              </Chip>
            ))}
        </div>
      </Section>

      <Section footer="После правки бот забудет свои прежние догадки и разберёт следующие траты заново. Ваши ручные исправления останутся.">
        <div className="sheet-actions">
          <Button size="l" stretched loading={busy} onClick={save}>
            Сохранить
          </Button>
        </div>
      </Section>
    </Sheet>
  );
}
