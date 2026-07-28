import { useState } from "react";
import { api, Category } from "./api";

/**
 * Правка категорий. Названия и подсказки уходят прямо в JSON-схему запроса
 * к модели: подсказка — единственный способ научить её различать то, что
 * путает именно нас («самокат» — это аренда или магазин?).
 *
 * Добавлять и удалять нельзя: категорий ровно четырнадцать (plan.md §14).
 */
export default function Categories({
  categories,
  onClose,
  onSaved,
}: {
  categories: Category[];
  onClose: () => void;
  onSaved: (c: Category) => void;
}) {
  const [editing, setEditing] = useState<Category | null>(null);

  return (
    <div className="backdrop" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="sheet" role="dialog" aria-modal="true">
        <div className="sheet__grip" />
        <h2 className="sheet__title">Категории</h2>
        <p className="sheet__hint">
          Подсказка уходит в запрос к модели — по ней она решает, куда отнести трату.
        </p>

        {editing ? (
          <CategoryForm
            category={editing}
            onCancel={() => setEditing(null)}
            onSaved={(c) => {
              onSaved(c);
              setEditing(null);
            }}
          />
        ) : (
          <div className="cats">
            {categories.map((c) => (
              <button key={c.id} className="cats__row" onClick={() => setEditing(c)}>
                <span className="cats__name">{c.name}</span>
                <span className="cats__hint">{c.hint || "без подсказки"}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function CategoryForm({
  category,
  onCancel,
  onSaved,
}: {
  category: Category;
  onCancel: () => void;
  onSaved: (c: Category) => void;
}) {
  const [name, setName] = useState(category.name);
  const [hint, setHint] = useState(category.hint);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const saved = await api.patchCategory(category.id, { name, hint });
      onSaved({ ...category, ...saved });
    } catch (err) {
      setError(err instanceof Error ? err.message : "не сохранилось");
      setBusy(false);
    }
  }

  return (
    <>
      <div className="field">
        <div className="field__label">Название</div>
        <input className="input" value={name} maxLength={32} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="field">
        <div className="field__label">Подсказка для модели</div>
        <input
          className="input"
          value={hint}
          maxLength={120}
          placeholder="что сюда относится"
          onChange={(e) => setHint(e.target.value)}
        />
      </div>
      {error && <div className="sheet__error">{error}</div>}
      <button className="btn" onClick={() => void save()} disabled={busy || !name.trim()}>
        {busy ? "Сохраняю…" : "Сохранить"}
      </button>
      <button className="btn btn--text" onClick={onCancel}>
        Назад к списку
      </button>
    </>
  );
}
