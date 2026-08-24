import { useState } from "react";
import { Button, Cell, Chip, Input, Section, SegmentedControl } from "@telegram-apps/telegram-ui";
import { ApiError, api, type Category, type Member, type Tx } from "./api";
import { money } from "./format";
import { ErrorBar, Sheet, Who } from "./ui";

const KINDS = [
  { id: "expense", label: "Трата" },
  { id: "income", label: "Поступление" },
  { id: "transfer", label: "Перевод" },
] as const;

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
  const [kind, setKind] = useState<Tx["kind"]>(tx.kind);
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
      onDone(
        await api.updateTx(tx.id, {
          amount,
          description,
          category_id: categoryID,
          clear_category: categoryID === null,
          recipients: kind === "expense" ? recipients : [],
          kind,
          updated_at: tx.updated_at ?? "",
        }),
      );
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
    <Sheet title="Трата" onClose={onClose}>
      {error && <ErrorBar text={error} onClose={() => setError("")} />}

      <Section>
        <Input
          header="Сумма"
          inputMode="decimal"
          value={amount}
          onChange={(e) => setAmount(e.target.value.replace(",", "."))}
        />
        <Input
          header="Что это было"
          value={description}
          placeholder="пятёрочка"
          onChange={(e) => setDescription(e.target.value)}
        />
      </Section>

      <Section header="Вид">
        <SegmentedControl>
          {KINDS.map((k) => (
            <SegmentedControl.Item key={k.id} selected={kind === k.id} onClick={() => setKind(k.id)}>
              {k.label}
            </SegmentedControl.Item>
          ))}
        </SegmentedControl>
      </Section>

      {kind !== "transfer" && (
        <Section header="Категория">
          <div className="chips">
            <Chip
              mode={categoryID === null ? "elevated" : "outline"}
              onClick={() => setCategoryID(null)}
            >
              Без категории
            </Chip>
            {categories.map((c) => (
              <Chip
                key={c.id}
                mode={categoryID === c.id ? "elevated" : "outline"}
                onClick={() => setCategoryID(c.id)}
              >
                {c.name}
              </Chip>
            ))}
          </div>
        </Section>
      )}

      {kind === "expense" && (
        <Section
          header="Кому"
          footer="Никого не выбрано — значит трата общая: в отчёте она стоит отдельной строкой и по людям не делится."
        >
          {/* Пустой выбор — это «на всех», а не «не выбрано». */}
          <Cell
            Component="label"
            after={<input type="radio" checked={recipients.length === 0} readOnly />}
            onClick={() => setRecipients([])}
          >
            На всех
          </Cell>
          {pickable.map((m) => (
            <Cell
              key={m.id}
              Component="label"
              before={<Who member={m} slot={slots.get(m.id) ?? 10} size={28} />}
              after={<input type="checkbox" checked={recipients.includes(m.id)} readOnly />}
              description={m.left ? "вышел из группы" : undefined}
              onClick={() => toggle(m.id)}
            >
              {m.name}
            </Cell>
          ))}
        </Section>
      )}

      <Section footer={`Записано как «${tx.raw_text}» · ${money(tx.amount)}`}>
        <div className="sheet-actions">
          <Button size="l" stretched loading={busy} onClick={save}>
            Сохранить
          </Button>
          <Button size="l" stretched mode="plain" disabled={busy} onClick={remove}>
            Удалить
          </Button>
        </div>
      </Section>
    </Sheet>
  );
}
