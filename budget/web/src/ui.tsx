import { ReactNode } from "react";
import { Avatar, Banner, Modal, Placeholder, Spinner } from "@telegram-apps/telegram-ui";
import { ModalHeader } from "@telegram-apps/telegram-ui/dist/components/Overlays/Modal/components/ModalHeader/ModalHeader";
import type { Member } from "./api";
import { api } from "./api";
import { initials } from "./format";

/**
 * Кружок участника: фото, если есть, иначе инициалы.
 *
 * Цвет слота остаётся своим, а не библиотечным: он связывает кружок с его
 * сегментом на графике, и без этой связи легенда перестаёт что-либо
 * объяснять. Всё остальное — размеры, скругление, шрифт — отдано Telegram.
 */
export function Who({
  member,
  slot,
  size = 28,
}: {
  member: Member | undefined;
  slot: number;
  size?: 20 | 24 | 28 | 40 | 48 | 96;
}) {
  if (!member) return <Avatar size={size} acronym="?" />;

  if (member.avatar_at) {
    return <Avatar size={size} src={api.avatarURL(member.user_id, member.avatar_at)} />;
  }
  return (
    <Avatar size={size} acronym={initials(member.name)} className={`who slot-${slot}`} />
  );
}

/** Лист снизу — единственная форма модального окна в приложении. */
export function Sheet({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <Modal
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      header={<ModalHeader>{title}</ModalHeader>}
    >
      {children}
    </Modal>
  );
}

/** Пустой экран с объяснением, а не молчаливая пустота. */
export function Empty({
  title,
  hint,
  action,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
}) {
  return <Placeholder header={title} description={hint} action={action} />;
}

/**
 * Ошибка. Держится, пока её не закроют: исчезающая ошибка — это ошибка,
 * которую не успели прочитать.
 */
export function ErrorBar({ text, onClose }: { text: string; onClose: () => void }) {
  return (
    <Banner type="inline" onCloseIcon={onClose} description={text} />
  );
}

/** Ожидание. Пустой экран без него читается как поломка. */
export function Loading() {
  return (
    <Placeholder>
      <Spinner size="l" />
    </Placeholder>
  );
}
