import { useEffect, useRef, useState } from "react";
import { api, Category, Conflict, Tx } from "./api";
import { money } from "./format";

const BENEFICIARIES = [
  { value: "payer", label: "👤 мне" },
  { value: "partner", label: "🧍 ей" },
  { value: "both", label: "👥 нам" },
] as const;

const KINDS = [
  { value: "expense", label: "трата" },
  { value: "transfer", label: "перевод" },
  { value: "income", label: "доход" },
] as const;

type Props = {
  tx: Tx | null; // null — новая запись
  categories: Category[];
  /** Дата новой записи: сегодня в текущем месяце, иначе первое число открытого. */
  defaultDay: string;
  /** Сегодняшний день — для чипов «сегодня/вчера» и потолка даты. */
  today: string;
  partnerName?: string;
  onClose: () => void;
  onSaved: (tx: Tx) => void;
  onDeleted: (tx: Tx) => void;
};

/**
 * Карточка операции: лист снизу на телефоне, модалка на десктопе.
 * Здесь правится сумма и дата — то, чего в боте нет вовсе.
 */
export default function Sheet({ tx, categories, defaultDay, today, partnerName, onClose, onSaved, onDeleted }: Props) {
  const [amount, setAmount] = useState(tx?.amount ?? "");
  const [description, setDescription] = useState(tx?.description ?? "");
  const [categoryID, setCategoryID] = useState<number | null>(tx?.category_id ?? null);
  const [beneficiary, setBeneficiary] = useState(tx?.beneficiary ?? "both");
  const [kind, setKind] = useState(tx?.kind ?? "expense");
  const [day, setDay] = useState(tx?.day ?? defaultDay);
  const [version, setVersion] = useState(tx?.updated_at ?? null);

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<Tx | null>(null);
  const [confirming, setConfirming] = useState(false);

  const readOnly = tx !== null && !tx.mine;
  // Сутки вперёд сервер разрешает (webapp.md §4): пусть и поле разрешает.
  const maxDay = shiftDay(today, 1);
  const transfer = kind === "transfer";

  const sheetRef = useRef<HTMLDivElement>(null);

  // Esc закрывает, Tab не уходит на список под подложкой, «назад» на
  // телефоне закрывает лист, а не уводит со страницы.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
        return;
      }
      if (e.key !== "Tab" || !sheetRef.current) return;

      const focusable = sheetRef.current.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };

    const onPop = () => onClose();

    document.addEventListener("keydown", onKey);
    window.addEventListener("popstate", onPop);
    document.body.style.overflow = "hidden";
    window.history.pushState({ sheet: true }, "");

    return () => {
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("popstate", onPop);
      document.body.style.overflow = "";
      // Закрыли не кнопкой «назад» — снимаем свою запись из истории.
      if (window.history.state?.sheet) window.history.back();
    };
  }, [onClose]);

  useEffect(() => {
    sheetRef.current?.querySelector<HTMLElement>("input, button")?.focus();
  }, []);

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const body = {
        amount,
        description,
        category_id: transfer ? undefined : categoryID ?? undefined,
        // Снятие категории просим явно и только если она была: иначе правка
        // суммы у неразобранной записи снимала бы её с очереди воркера.
        clear_category: !transfer && categoryID === null && tx?.category_id != null,
        beneficiary,
        kind,
        spent_at: day,
      };
      const saved = tx
        ? await api.patch(tx.id, { ...body, updated_at: version })
        : await api.create(body);
      onSaved(saved);
      onClose();
    } catch (err) {
      if (err instanceof Conflict) {
        // Запись изменили, пока её правили: показываем оба варианта,
        // а не затираем чужую работу молча.
        setConflict(err.current);
      } else {
        setError(err instanceof Error ? err.message : "не сохранилось");
      }
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!tx) return;
    setBusy(true);
    try {
      await api.remove(tx.id);
      onDeleted(tx);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "не удалилось");
      setBusy(false);
    }
  }

  if (conflict) {
    return (
      <Backdrop onClose={onClose}>
        <div className="sheet" role="dialog" aria-modal="true">
          <h2 className="sheet__title">Запись изменилась</h2>
          <p className="sheet__hint">
            Пока ты правил, её поменяли — в боте или воркером. Что оставить?
          </p>
          <div className="sheet__compare">
            <div>
              <div className="sheet__hint">Сейчас в базе</div>
              <div className="sheet__value">{money(conflict.amount)}</div>
              <div className="sheet__hint">
                {conflict.description} · {conflict.category || "без категории"}
              </div>
            </div>
            <div>
              <div className="sheet__hint">Твоя правка</div>
              <div className="sheet__value">{money(amount || "0")}</div>
              <div className="sheet__hint">
                {description} · {categories.find((c) => c.id === categoryID)?.name || "без категории"}
              </div>
            </div>
          </div>
          <button
            className="btn"
            onClick={() => {
              setVersion(conflict.updated_at);
              setConflict(null);
            }}
          >
            Оставить мою правку
          </button>
          <button
            className="btn btn--text"
            onClick={() => {
              onSaved(conflict);
              onClose();
            }}
          >
            Взять то, что в базе
          </button>
        </div>
      </Backdrop>
    );
  }

  return (
    <Backdrop onClose={onClose}>
      <div className="sheet" role="dialog" aria-modal="true" ref={sheetRef}>
        <div className="sheet__grip" />
        <h2 className="sheet__title">
          {tx ? (transfer ? "Перевод" : kind === "income" ? "Поступление" : "Трата") : "Новая запись"}
        </h2>

        {readOnly && (
          <div className="sheet__badge">Запись {partnerName ?? "партнёра"} — можно только смотреть</div>
        )}

        <label className="sheet__amount">
          <input
            inputMode="decimal"
            value={amount}
            disabled={readOnly}
            onChange={(e) => setAmount(e.target.value)}
            aria-label="Сумма"
          />
          <span>₽</span>
        </label>

        <Field label="Описание">
          <input
            className="input"
            value={description}
            disabled={readOnly}
            maxLength={64}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>

        {!transfer && (
          <Field label="Категория">
            <div className="chips">
              {categories.map((c) => (
                <button
                  key={c.id}
                  className={`chip${categoryID === c.id ? " chip--on" : ""}`}
                  disabled={readOnly}
                  onClick={() => setCategoryID(categoryID === c.id ? null : c.id)}
                >
                  {c.name}
                </button>
              ))}
            </div>
          </Field>
        )}

        {!transfer && (
          <Field label="На кого">
            <Segmented
              options={BENEFICIARIES}
              value={beneficiary}
              disabled={readOnly}
              onChange={setBeneficiary}
            />
          </Field>
        )}

        <Field label="Тип">
          <Segmented options={KINDS} value={kind} disabled={readOnly} onChange={setKind} />
        </Field>

        <Field label="Когда">
          <div className="chips">
            <button
              className={`chip${day === today ? " chip--on" : ""}`}
              disabled={readOnly}
              onClick={() => setDay(today)}
            >
              сегодня
            </button>
            <button
              className={`chip${day === shiftDay(today, -1) ? " chip--on" : ""}`}
              disabled={readOnly}
              onClick={() => setDay(shiftDay(today, -1))}
            >
              вчера
            </button>
            <input
              className="input input--date"
              type="date"
              value={day}
              max={maxDay}
              disabled={readOnly}
              onChange={(e) => e.target.value && setDay(e.target.value)}
              aria-label="Дата"
            />
          </div>
        </Field>

        {tx && tx.raw_text && (
          <p className="sheet__hint">из сообщения: «{tx.raw_text}»</p>
        )}
        {tx?.updated_at && (
          <p className="sheet__hint">исправлено {shortStamp(tx.updated_at)}</p>
        )}

        {error && <div className="sheet__error">{error}</div>}

        {!readOnly && (
          <>
            <button className="btn" onClick={() => void save()} disabled={busy}>
              {busy ? "Сохраняю…" : "Сохранить"}
            </button>
            {tx &&
              (confirming ? (
                <div className="sheet__confirm">
                  <span>Удалить запись?</span>
                  <button className="btn btn--danger" onClick={() => void remove()} disabled={busy}>
                    Удалить
                  </button>
                  <button className="btn btn--text" onClick={() => setConfirming(false)}>
                    Отмена
                  </button>
                </div>
              ) : (
                <button className="btn btn--danger btn--text" onClick={() => setConfirming(true)}>
                  Удалить
                </button>
              ))}
          </>
        )}
      </div>
    </Backdrop>
  );
}

function Backdrop({ children, onClose }: { children: React.ReactNode; onClose: () => void }) {
  return (
    <div
      className="backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      {children}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="field">
      <div className="field__label">{label}</div>
      {children}
    </div>
  );
}

function Segmented<T extends string>({
  options,
  value,
  disabled,
  onChange,
}: {
  options: readonly { value: T; label: string }[];
  value: string;
  disabled?: boolean;
  onChange: (v: T) => void;
}) {
  return (
    <div className="segmented">
      {options.map((o) => (
        <button
          key={o.value}
          className={`segmented__item${value === o.value ? " segmented__item--on" : ""}`}
          disabled={disabled}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

function shiftDay(day: string, delta: number): string {
  const [y, m, d] = day.split("-").map(Number);
  const date = new Date(Date.UTC(y, m - 1, d + delta));
  return date.toISOString().slice(0, 10);
}

function shortStamp(iso: string): string {
  const d = new Date(iso);
  return `${String(d.getDate()).padStart(2, "0")}.${String(d.getMonth() + 1).padStart(2, "0")} в ${String(
    d.getHours(),
  ).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}
