import { useState } from "react";
import { Button, Cell, Chip, Input, Section } from "@telegram-apps/telegram-ui";
import { ApiError, api, type Category, type Member } from "./api";
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
        footer="Подсказка уезжает боту вместе с сообщением и помогает ему разбирать траты точнее."
      >
        {categories.map((c) => (
          <Cell
            key={c.id}
            multiline
            subtitle={c.hint || undefined}
            description={
              c.default_to !== null
                ? `по умолчанию — ${byID.get(c.default_to)?.name ?? "кому-то"}`
                : undefined
            }
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
  const [defaultTo, setDefaultTo] = useState<number | null>(category?.default_to ?? null);
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
        <Input
          header="Подсказка боту"
          placeholder="пиво, вино, крепкое"
          value={hint}
          onChange={(e) => setHint(e.target.value)}
        />
      </Section>

      <Section
        header="Кому по умолчанию"
        footer="«Косметика — Уле» верно и когда платит не Уля. Без адресата трата уходит тому, кто её записал."
      >
        <div className="chips">
          <Chip mode={defaultTo === null ? "elevated" : "outline"} onClick={() => setDefaultTo(null)}>
            Тому, кто платил
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

      <Section footer="После правки категорий бот забывает свои прежние догадки — но не то, что вы поправили руками.">
        <div className="sheet-actions">
          <Button size="l" stretched loading={busy} onClick={save}>
            Сохранить
          </Button>
        </div>
      </Section>
    </Sheet>
  );
}
