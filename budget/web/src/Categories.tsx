import { useState } from "react";
import { api, Beneficiary, Category } from "./api";

const DEFAULTS = [
  { value: "payer", label: "мне" },
  { value: "partner", label: "ей" },
  { value: "both", label: "на двоих" },
] as const;

const defaultLabel = (b: string) => DEFAULTS.find((d) => d.value === b)?.label ?? "на двоих";

/**
 * Правка категорий. Названия и подсказки уходят прямо в JSON-схему запроса
 * к модели: подсказка — единственный способ научить её различать то, что
 * путает именно нас («самокат» — это аренда или магазин?).
 *
 * Свои категории добавлять можно — но каждая удлиняет промпт и усложняет
 * модели выбор, поэтому список стоит держать коротким.
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
  const [creating, setCreating] = useState(false);

  return (
    <div className="backdrop" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="sheet" role="dialog" aria-modal="true">
        <div className="sheet__grip" />
        <h2 className="sheet__title">
          {editing ? editing.name : creating ? "Новая категория" : "Категории"}
        </h2>
        {!editing && !creating && (
          <p className="sheet__hint">
            Подсказка уходит в запрос к модели — по ней она решает, куда отнести трату.
            «По умолчанию» побеждает догадку: если в сообщении не сказано, на кого
            потрачено, берётся оно.
          </p>
        )}

        {editing || creating ? (
          <CategoryForm
            category={editing}
            onCancel={() => {
              setEditing(null);
              setCreating(false);
            }}
            onSaved={(c) => {
              onSaved(c);
              setEditing(null);
              setCreating(false);
            }}
          />
        ) : (
          <>
            <div className="cats">
              {categories.map((c) => (
                <button key={c.id} className="cats__row" onClick={() => setEditing(c)}>
                  <span className="cats__name">
                    {c.name}
                    <span className="cats__default">{defaultLabel(c.beneficiary)}</span>
                  </span>
                  <span className="cats__hint">{c.hint || "без подсказки"}</span>
                </button>
              ))}
            </div>
            <button className="btn" onClick={() => setCreating(true)}>
              Добавить категорию
            </button>
          </>
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
  category: Category | null;
  onCancel: () => void;
  onSaved: (c: Category) => void;
}) {
  const [name, setName] = useState(category?.name ?? "");
  const [hint, setHint] = useState(category?.hint ?? "");
  const [beneficiary, setBeneficiary] = useState<Beneficiary>(category?.beneficiary ?? "both");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const saved = category
        ? await api.patchCategory(category.id, { name, hint, beneficiary })
        : await api.createCategory({ name, hint, beneficiary });
      onSaved({ ...(category ?? {}), ...saved } as Category);
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
      <div className="field">
        <div className="field__label">По умолчанию потрачено</div>
        <div className="segmented">
          {DEFAULTS.map((d) => (
            <button
              key={d.value}
              className={`segmented__item${beneficiary === d.value ? " segmented__item--on" : ""}`}
              onClick={() => setBeneficiary(d.value)}
            >
              {d.label}
            </button>
          ))}
        </div>
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
