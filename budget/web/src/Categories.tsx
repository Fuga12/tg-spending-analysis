import { useState } from "react";
import { ApiError, api, type Category, type Member } from "./api";
import { Sheet } from "./ui";

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
    <div className="screen">
      <header className="screen__head">
        <h1>Категории</h1>
        <p className="screen__sub">Подсказка помогает боту разбирать сообщения точнее.</p>
      </header>

      <ul className="cats">
        {categories.map((c) => (
          <li key={c.id}>
            <button className="cats__item" onClick={() => setEditing(c)}>
              <span className="cats__name">{c.name}</span>
              {c.hint && <span className="cats__hint">{c.hint}</span>}
              {c.default_to !== null && (
                <span className="cats__to">по умолчанию — {byID.get(c.default_to)?.name ?? "кому-то"}</span>
              )}
            </button>
          </li>
        ))}
      </ul>

      <button className="btn btn--primary" onClick={() => setEditing("new")}>
        Добавить категорию
      </button>

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
    </div>
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
    <Sheet
      title={category ? "Категория" : "Новая категория"}
      onClose={onClose}
      foot={
        <button className="btn btn--primary" onClick={save} disabled={busy}>
          Сохранить
        </button>
      }
    >
      {error && <p className="field__error">{error}</p>}

      <label className="field">
        <span className="field__label">Название</span>
        <input className="field__input" value={name} onChange={(e) => setName(e.target.value)} />
      </label>

      <label className="field">
        <span className="field__label">Подсказка боту</span>
        <input
          className="field__input"
          value={hint}
          onChange={(e) => setHint(e.target.value)}
          placeholder="пиво, вино, крепкое"
        />
      </label>

      <div className="field">
        <span className="field__label">Кому по умолчанию</span>
        {/* «Косметика — Уле» верно и когда платит не Уля. Без адресата
            трата уходит тому, кто её записал. */}
        <div className="chips">
          <button
            className={`chip${defaultTo === null ? " chip--on" : ""}`}
            onClick={() => setDefaultTo(null)}
          >
            Тому, кто платил
          </button>
          {members
            .filter((m) => !m.left)
            .map((m) => (
              <button
                key={m.id}
                className={`chip${defaultTo === m.id ? " chip--on" : ""}`}
                onClick={() => setDefaultTo(m.id)}
              >
                {m.name}
              </button>
            ))}
        </div>
      </div>

      <p className="field__hint">
        После правки категорий бот забывает свои прежние догадки — но не то,
        что вы поправили руками.
      </p>
    </Sheet>
  );
}
