import { ReactNode, useEffect } from "react";
import type { Member } from "./api";
import { api } from "./api";
import { initials } from "./format";

/** Кружок участника: фото, если есть, иначе инициалы в его цвете. */
export function Who({
  member,
  slot,
  size = 24,
}: {
  member: Member | undefined;
  slot: number;
  size?: number;
}) {
  if (!member) return <i className="who slot-10" style={{ width: size, height: size }} />;

  return (
    <i className={`who slot-${slot}`} style={{ width: size, height: size }} title={member.name}>
      {member.avatar_at ? (
        <img className="who__img" src={api.avatarURL(member.user_id, member.avatar_at)} alt="" />
      ) : (
        initials(member.name)
      )}
    </i>
  );
}

/**
 * Лист снизу — единственная форма модального окна в приложении.
 *
 * Закрывается по фону и по Escape: на телефоне первое, что пробует человек,
 * — тап мимо, и окно, которое так не закрывается, ощущается сломанным.
 */
export function Sheet({
  title,
  onClose,
  children,
  foot,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  foot?: ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    // Фон не должен прокручиваться под открытым листом.
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [onClose]);

  return (
    <div className="scrim" onClick={onClose}>
      <section className="sheet" onClick={(e) => e.stopPropagation()} role="dialog" aria-label={title}>
        <header className="sheet__head">
          <h2>{title}</h2>
          <button className="sheet__close" onClick={onClose} aria-label="Закрыть">
            ✕
          </button>
        </header>
        <div className="sheet__body">{children}</div>
        {foot && <footer className="sheet__foot">{foot}</footer>}
      </section>
    </div>
  );
}

/** Пустой экран с объяснением, а не молчаливая пустота. */
export function Empty({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="empty">
      <p className="empty__title">{title}</p>
      {hint && <p className="empty__hint">{hint}</p>}
    </div>
  );
}

/** Полоска ошибки. Держится, пока её не закроют: исчезающая ошибка — это
 *  ошибка, которую не успели прочитать. */
export function ErrorBar({ text, onClose }: { text: string; onClose: () => void }) {
  return (
    <div className="errorbar" role="alert">
      <span>{text}</span>
      <button onClick={onClose} aria-label="Закрыть">
        ✕
      </button>
    </div>
  );
}
