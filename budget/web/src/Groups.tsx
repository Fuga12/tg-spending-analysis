import { useState } from "react";
import { api, BeneficiaryGroup } from "./api";

export default function Groups({
  groups,
  onClose,
  onChanged,
}: {
  groups: BeneficiaryGroup[];
  onClose: () => void;
  onChanged: (groups: BeneficiaryGroup[]) => void;
}) {
  const [editing, setEditing] = useState<BeneficiaryGroup | null>(null);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const beginEdit = (group: BeneficiaryGroup) => {
    setEditing(group);
    setCreating(false);
    setName(group.name);
    setError(null);
  };

  const beginCreate = () => {
    setEditing(null);
    setCreating(true);
    setName("");
    setError(null);
  };

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const saved = editing
        ? await api.patchBeneficiaryGroup(editing.id, name)
        : await api.createBeneficiaryGroup(name);
      onChanged(
        editing
          ? groups.map((g) => (g.id === saved.id ? saved : g))
          : [...groups, saved],
      );
      setEditing(null);
      setCreating(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "не сохранилось");
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!editing) return;
    setBusy(true);
    setError(null);
    try {
      await api.deleteBeneficiaryGroup(editing.id);
      onChanged(groups.filter((g) => g.id !== editing.id));
      setEditing(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "не удалилось");
    } finally {
      setBusy(false);
    }
  }

  const form = editing !== null || creating;
  return (
    <div className="backdrop" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="sheet" role="dialog" aria-modal="true">
        <div className="sheet__grip" />
        <h2 className="sheet__title">
          {editing ? editing.name : creating ? "Новая группа" : "На кого тратим"}
        </h2>
        {!form && (
          <p className="sheet__hint">
            Эти группы дополняют «на двоих», Улю и Илью. Например, сюда удобно
            вынести подарки или расходы на родных.
          </p>
        )}

        {form ? (
          <>
            <div className="field">
              <div className="field__label">Название</div>
              <input
                className="input"
                value={name}
                maxLength={32}
                autoFocus
                placeholder="Например, Подарки"
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && name.trim() && void save()}
              />
            </div>
            {error && <div className="sheet__error">{error}</div>}
            <button className="btn" onClick={() => void save()} disabled={busy || !name.trim()}>
              {busy ? "Сохраняю…" : "Сохранить"}
            </button>
            {editing && (
              <button className="btn btn--danger btn--text" onClick={() => void remove()} disabled={busy}>
                Удалить группу
              </button>
            )}
            <button
              className="btn btn--text"
              onClick={() => {
                setEditing(null);
                setCreating(false);
                setError(null);
              }}
            >
              Назад к списку
            </button>
          </>
        ) : (
          <>
            <div className="cats">
              {groups.map((group) => (
                <button key={group.id} className="cats__row" onClick={() => beginEdit(group)}>
                  <span className="cats__name">{group.name}</span>
                  <span className="cats__hint">переименовать</span>
                </button>
              ))}
            </div>
            <button className="btn" onClick={beginCreate}>Добавить группу</button>
          </>
        )}
      </div>
    </div>
  );
}
