import { useState } from "react";
import { ApiError, api, type Category, type Member, type Tx } from "./api";
import { money } from "./format";
import { Sheet, Who } from "./ui";

/**
 * Правка траты.
 *
 * Здесь же выбираются получатели — единственное место, где это вообще можно
 * сделать: модель называет их по имени, но ошибается, а в чате выбор из
 * десяти человек превращается в дерево нажатий.
 */
export function TxEdit({
  tx,
  members,
  categories,
  slots,
  onDone,
  onClose,
}: {
  tx: Tx;
  members: Member[];
  categories: Category[];
  slots: Map<number, number>;
  onDone: (next: Tx | null) => void;
  onClose: () => void;
}) {
  const [amount, setAmount] = useState(tx.amount);
  const [description, setDescription] = useState(tx.description);
  const [categoryID, setCategoryID] = useState<number | null>(tx.category_id);
  const [recipients, setRecipients] = useState<number[]>(tx.recipients);
  const [kind, setKind] = useState(tx.kind);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Ушедшие участники в выбор не попадают: назначить трату тому, кого в
  // группе нет, нельзя. Но уже назначенного показать надо — иначе правка
  // молча его снимет.
  const pickable = members.filter((m) => !m.left || recipients.includes(m.id));

  const toggle = (id: number) =>
    setRecipients((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const next = await api.updateTx(tx.id, {
        amount,
        description,
        category_id: categoryID,
        clear_category: categoryID === null,
        recipients: kind === "expense" ? recipients : [],
        kind,
        updated_at: tx.updated_at ?? "",
      });
      onDone(next);
    } catch (e) {
      // Конфликт версий — не поломка, а другой человек, правивший ту же
      // трату. Об этом надо сказать словами, а не «попробуйте позже».
      setError(e instanceof ApiError ? e.message : "Не смог сохранить.");
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      await api.deleteTx(tx.id);
      onDone(null);
    } catch {
      setError("Не смог удалить.");
      setBusy(false);
    }
  };

  return (
    <Sheet
      title="Трата"
      onClose={onClose}
      foot={
        <>
          <button className="btn btn--primary" onClick={save} disabled={busy}>
            Сохранить
          </button>
          <button className="btn foot__danger" onClick={remove} disabled={busy}>
            Удалить
          </button>
        </>
      }
    >
      {error && <p className="field__error">{error}</p>}

      <label className="field">
        <span className="field__label">Сумма</span>
        <input
          className="field__input"
          inputMode="decimal"
          value={amount}
          onChange={(e) => setAmount(e.target.value.replace(",", "."))}
        />
      </label>

      <label className="field">
        <span className="field__label">Что это было</span>
        <input
          className="field__input"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="пятёрочка"
        />
      </label>

      <div className="field">
        <span className="field__label">Вид</span>
        <div className="chips">
          {(["expense", "income", "transfer"] as const).map((k) => (
            <button
              key={k}
              className={`chip${kind === k ? " chip--on" : ""}`}
              onClick={() => setKind(k)}
            >
              {k === "expense" ? "Трата" : k === "income" ? "Поступление" : "Перевод"}
            </button>
          ))}
        </div>
      </div>

      {kind !== "transfer" && (
        <div className="field">
          <span className="field__label">Категория</span>
          <div className="chips">
            <button
              className={`chip${categoryID === null ? " chip--on" : ""}`}
              onClick={() => setCategoryID(null)}
            >
              Без категории
            </button>
            {categories.map((c) => (
              <button
                key={c.id}
                className={`chip${categoryID === c.id ? " chip--on" : ""}`}
                onClick={() => setCategoryID(c.id)}
              >
                {c.name}
              </button>
            ))}
          </div>
        </div>
      )}

      {kind === "expense" && (
        <div className="field">
          <span className="field__label">Кому</span>
          {/* Пустой выбор — это «на всех», а не «не выбрано»: общая трата
              не раскладывается по людям и стоит в отчёте отдельной строкой. */}
          <button
            className={`chip${recipients.length === 0 ? " chip--on" : ""}`}
            onClick={() => setRecipients([])}
          >
            На всех
          </button>
          <div className="who-list">
            {pickable.map((m) => (
              <button
                key={m.id}
                className={`who-list__item${recipients.includes(m.id) ? " who-list__item--on" : ""}`}
                onClick={() => toggle(m.id)}
              >
                <Who member={m} slot={slots.get(m.id) ?? 10} />
                <span>{m.name}</span>
                {m.left && <em className="who-list__left">вышел</em>}
              </button>
            ))}
          </div>
        </div>
      )}

      <p className="field__hint">
        Записано как «{tx.raw_text}» · {money(tx.amount)}
      </p>
    </Sheet>
  );
}
