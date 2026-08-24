import { useState } from "react";
import { Button, Cell, Input, Section } from "@telegram-apps/telegram-ui";
import { ApiError, api, type State } from "./api";
import { plural } from "./format";
import { Empty, ErrorBar, Sheet, Who } from "./ui";

/**
 * Группа: состав, роли, приглашения.
 *
 * Зовут по telegram id, а не по логину: приглашение по логину некуда
 * доставить, а связать его с аккаунтом можно только на честном слове.
 */
export function GroupScreen({
  state,
  slots,
  onChanged,
}: {
  state: State;
  slots: Map<number, number>;
  onChanged: () => void;
}) {
  const [inviting, setInviting] = useState(false);
  const [error, setError] = useState("");

  const group = state.group!;
  const amAdmin = group.role === "admin";
  const active = state.members.filter((m) => !m.left);
  const left = state.members.filter((m) => m.left);
  const full = active.length >= state.max_members;

  const act = async (fn: () => Promise<unknown>) => {
    setError("");
    try {
      await fn();
      onChanged();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Не получилось.");
    }
  };

  const confirmAnd = (question: string, fn: () => Promise<unknown>) => {
    if (window.confirm(question)) void act(fn);
  };

  return (
    <>
      {error && <ErrorBar text={error} onClose={() => setError("")} />}

      <Section
        header={group.name}
        footer={`${active.length} ${plural(active.length, "участник", "участника", "участников")} из ${state.max_members}`}
      >
        {active.map((m) => (
          <Cell
            key={m.id}
            before={<Who member={m} slot={slots.get(m.id) ?? 10} size={40} />}
            description={m.role === "admin" ? "администратор" : undefined}
            after={
              amAdmin && m.id !== group.member_id ? (
                <span className="row-actions">
                  <Button
                    size="s"
                    mode="bezeled"
                    onClick={() => act(() => api.setRole(m.id, m.role === "admin" ? "member" : "admin"))}
                  >
                    {m.role === "admin" ? "Разжаловать" : "В админы"}
                  </Button>
                  <Button
                    size="s"
                    mode="plain"
                    onClick={() => confirmAnd(`Исключить ${m.name}?`, () => api.removeMember(m.id))}
                  >
                    Исключить
                  </Button>
                </span>
              ) : undefined
            }
          >
            {m.name}
            {m.id === group.member_id && " · это ты"}
          </Cell>
        ))}
      </Section>

      {left.length > 0 && (
        // Ушедшие показаны отдельно: их траты остались в истории, и без
        // этого списка непонятно, откуда в отчёте берётся их имя.
        <Section header="Были в группе">
          {left.map((m) => (
            <Cell key={m.id} before={<Who member={m} slot={slots.get(m.id) ?? 10} size={40} />}>
              {m.name}
            </Cell>
          ))}
        </Section>
      )}

      <Section>
        {amAdmin && (
          <div className="sheet-actions">
            <Button size="l" stretched disabled={full} onClick={() => setInviting(true)}>
              {full ? "Группа заполнена" : "Пригласить"}
            </Button>
          </div>
        )}
        <div className="sheet-actions">
          <Button size="l" stretched mode="plain" onClick={() => confirmAnd("Выйти из группы?", () => api.leave())}>
            Выйти из группы
          </Button>
        </div>
      </Section>

      {inviting && (
        <InviteSheet
          onClose={() => setInviting(false)}
          onDone={() => {
            setInviting(false);
            onChanged();
          }}
        />
      )}
    </>
  );
}

function InviteSheet({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [id, setID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [sent, setSent] = useState("");

  const send = async () => {
    const userID = Number(id.trim());
    if (!Number.isInteger(userID) || userID <= 0) {
      setError("Это не похоже на telegram id.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const inv = await api.invite(userID);
      setSent(`Приглашение отправлено. Действует до ${new Date(inv.expires_at).toLocaleDateString("ru")}.`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Не получилось пригласить.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Sheet title="Пригласить" onClose={onClose}>
      {sent ? (
        <Section footer={sent}>
          <div className="sheet-actions">
            <Button size="l" stretched onClick={onDone}>
              Готово
            </Button>
          </div>
        </Section>
      ) : (
        <Section footer="Позвать можно только того, кто уже писал боту: приглашение приходит в тот же чат. Свой id человек узнаёт у @userinfobot.">
          <Input
            header="Telegram id"
            inputMode="numeric"
            placeholder="123456789"
            status={error ? "error" : "default"}
            value={id}
            onChange={(e) => setID(e.target.value)}
          />
          {error && <ErrorBar text={error} onClose={() => setError("")} />}
          <div className="sheet-actions">
            <Button size="l" stretched loading={busy} onClick={send}>
              Пригласить
            </Button>
          </div>
        </Section>
      )}
    </Sheet>
  );
}

/**
 * Экран для тех, кто ещё нигде не состоит: принять приглашение или завести
 * свою группу. Третьего варианта нет.
 */
export function NoGroupScreen({ state, onChanged }: { state: State; onChanged: () => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      onChanged();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Не получилось.");
      setBusy(false);
    }
  };

  return (
    <>
      {error && <ErrorBar text={error} onClose={() => setError("")} />}

      {state.invites.length > 0 ? (
        <Section header="Тебя зовут">
          {state.invites.map((inv) => (
            <Cell
              key={inv.id}
              multiline
              subtitle={`${inv.inviter} зовёт в «${inv.group_name}»`}
              after={
                <span className="row-actions">
                  <Button size="s" disabled={busy} onClick={() => act(() => api.acceptInvite(inv.id))}>
                    Принять
                  </Button>
                  <Button size="s" mode="plain" disabled={busy} onClick={() => act(() => api.declineInvite(inv.id))}>
                    Нет
                  </Button>
                </span>
              }
            >
              {inv.group_name}
            </Cell>
          ))}
        </Section>
      ) : (
        <Empty
          title="Ты пока не в группе"
          hint="Бюджет принадлежит группе: заведи свою или попроси администратора позвать тебя."
        />
      )}

      <Section header="Своя группа">
        <Input
          header="Название"
          placeholder="Дом"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <div className="sheet-actions">
          <Button size="l" stretched loading={busy} onClick={() => act(() => api.createGroup(name))}>
            Создать группу
          </Button>
        </div>
      </Section>
    </>
  );
}
