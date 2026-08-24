import { useState } from "react";
import { ApiError, api, type State } from "./api";
import { plural } from "./format";
import { Empty, Sheet, Who } from "./ui";

/**
 * Группа: состав, роли, приглашения.
 *
 * Зовут по telegram id, а не по логину: приглашение по логину некуда
 * доставить, а связать его с аккаунтом можно только на честном слове. Узнать
 * свой id человек может у любого бота вроде @userinfobot — это одна пересылка.
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
    if (!window.confirm(question)) return;
    void act(fn);
  };

  return (
    <div className="screen">
      <header className="screen__head">
        <h1>{group.name}</h1>
        <p className="screen__sub">
          {active.length} {plural(active.length, "участник", "участника", "участников")} из{" "}
          {state.max_members}
        </p>
      </header>

      {error && <p className="field__error">{error}</p>}

      <ul className="members">
        {active.map((m) => (
          <li key={m.id} className="members__item">
            <Who member={m} slot={slots.get(m.id) ?? 10} size={32} />
            <span className="members__name">
              {m.name}
              {m.id === group.member_id && <em> · это ты</em>}
            </span>
            {m.role === "admin" && <span className="members__role">админ</span>}

            {amAdmin && m.id !== group.member_id && (
              <span className="members__actions">
                <button
                  onClick={() =>
                    act(() => api.setRole(m.id, m.role === "admin" ? "member" : "admin"))
                  }
                >
                  {m.role === "admin" ? "Разжаловать" : "Сделать админом"}
                </button>
                <button
                  className="members__danger"
                  onClick={() => confirmAnd(`Исключить ${m.name}?`, () => api.removeMember(m.id))}
                >
                  Исключить
                </button>
              </span>
            )}
          </li>
        ))}
      </ul>

      {left.length > 0 && (
        <>
          <h2 className="screen__section">Были в группе</h2>
          {/* Ушедшие показаны отдельно: их траты остались в истории, и без
              этого списка непонятно, откуда в отчёте берётся их имя. */}
          <ul className="members members--muted">
            {left.map((m) => (
              <li key={m.id} className="members__item">
                <Who member={m} slot={slots.get(m.id) ?? 10} size={32} />
                <span className="members__name">{m.name}</span>
              </li>
            ))}
          </ul>
        </>
      )}

      <div className="screen__actions">
        {amAdmin && (
          <button className="btn btn--primary" disabled={full} onClick={() => setInviting(true)}>
            {full ? "Группа заполнена" : "Пригласить"}
          </button>
        )}
        <button
          className="btn foot__danger"
          onClick={() => confirmAnd("Выйти из группы?", () => api.leave())}
        >
          Выйти из группы
        </button>
      </div>

      {inviting && (
        <InviteSheet
          onClose={() => setInviting(false)}
          onDone={() => {
            setInviting(false);
            onChanged();
          }}
        />
      )}
    </div>
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
      setSent(`Приглашение отправлено. Оно действует до ${new Date(inv.expires_at).toLocaleDateString("ru")}.`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Не получилось пригласить.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Sheet
      title="Пригласить"
      onClose={onClose}
      foot={
        sent ? (
          <button className="btn btn--primary" onClick={onDone}>
            Готово
          </button>
        ) : (
          <button className="btn btn--primary" onClick={send} disabled={busy}>
            Пригласить
          </button>
        )
      }
    >
      {sent ? (
        <p className="field__hint">{sent}</p>
      ) : (
        <>
          <label className="field">
            <span className="field__label">Telegram id</span>
            <input
              className="field__input"
              inputMode="numeric"
              value={id}
              onChange={(e) => setID(e.target.value)}
              placeholder="123456789"
            />
          </label>
          <p className="field__hint">
            Позвать можно только того, кто уже писал боту: приглашение приходит
            в тот же чат. Свой id человек узнаёт у @userinfobot.
          </p>
          {error && <p className="field__error">{error}</p>}
        </>
      )}
    </Sheet>
  );
}

/** Экран для тех, кто ещё нигде не состоит: принять приглашение или завести
 *  свою группу. Третьего варианта нет. */
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
    <div className="screen">
      <header className="screen__head">
        <h1>Бюджет</h1>
      </header>

      {error && <p className="field__error">{error}</p>}

      {state.invites.length > 0 && (
        <>
          <h2 className="screen__section">Тебя зовут</h2>
          <ul className="invites">
            {state.invites.map((inv) => (
              <li key={inv.id} className="invites__item">
                <span>
                  <b>{inv.inviter}</b> зовёт в «{inv.group_name}»
                </span>
                <span className="invites__actions">
                  <button
                    className="btn btn--primary"
                    disabled={busy}
                    onClick={() => act(() => api.acceptInvite(inv.id))}
                  >
                    Принять
                  </button>
                  <button disabled={busy} onClick={() => act(() => api.declineInvite(inv.id))}>
                    Отказаться
                  </button>
                </span>
              </li>
            ))}
          </ul>
        </>
      )}

      {state.invites.length === 0 && (
        <Empty
          title="Ты пока не в группе"
          hint="Бюджет принадлежит группе: заведи свою или попроси администратора позвать тебя."
        />
      )}

      <h2 className="screen__section">Своя группа</h2>
      <label className="field">
        <span className="field__label">Название</span>
        <input
          className="field__input"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Дом"
        />
      </label>
      <button
        className="btn btn--primary"
        disabled={busy}
        onClick={() => act(() => api.createGroup(name))}
      >
        Создать группу
      </button>
    </div>
  );
}
