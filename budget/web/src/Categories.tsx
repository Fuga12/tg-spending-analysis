import { useState } from "react";
import { api, Beneficiary, Category, Me } from "./api";

/**
 * Варианты умолчания: два относительных и по одному на каждого участника.
 *
 * «Себе» и «на двоих» зависят от того, кто платил, а «Уле» — нет: косметика
 * достаётся Уле, кто бы её ни купил. Относительными значениями это не
 * выражается, поэтому у категории есть отдельный адресат-человек.
 */
function defaultOptions(me: Me | null) {
  const people = me
    ? [me.partner, { id: me.id, name: me.name, dative: me.dative }].filter(
        (p): p is NonNullable<typeof p> => p !== null,
      )
    : [];
  return [
    { key: "payer", label: "себе" },
    { key: "both", label: "на двоих" },
    ...people.map((p) => ({ key: `user:${p.id}`, label: p.dative })),
  ];
}

const defaultKey = (c: { beneficiary: string; user_id: number | null }) =>
  c.user_id !== null ? `user:${c.user_id}` : c.beneficiary;

const defaultLabel = (c: Category, me: Me | null) =>
  defaultOptions(me).find((o) => o.key === defaultKey(c))?.label ?? "на двоих";

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
  me,
  onClose,
  onSaved,
}: {
  categories: Category[];
  me: Me | null;
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
            me={me}
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
                    <span className="cats__default">{defaultLabel(c, me)}</span>
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
  me,
  onCancel,
  onSaved,
}: {
  category: Category | null;
  me: Me | null;
  onCancel: () => void;
  onSaved: (c: Category) => void;
}) {
  const [name, setName] = useState(category?.name ?? "");
  const [hint, setHint] = useState(category?.hint ?? "");
  const [target, setTarget] = useState(
    category ? defaultKey(category) : "both",
  );
  const options = defaultOptions(me);
  const userID = target.startsWith("user:") ? Number(target.slice(5)) : null;
  const beneficiary: Beneficiary = userID !== null ? "payer" : (target as Beneficiary);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const body = { name, hint, beneficiary, user_id: userID };
      const saved = category
        ? await api.patchCategory(category.id, body)
        : await api.createCategory(body);
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
        <div className="chips">
          {options.map((o) => (
            <button
              key={o.key}
              className={`chip${target === o.key ? " chip--on" : ""}`}
              onClick={() => setTarget(o.key)}
            >
              {o.label}
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
