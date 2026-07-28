import { useState } from "react";
import { initials } from "./format";

/**
 * Фото профиля из Telegram. Раздаёт наш сервер: отдавать браузеру токен бота
 * нельзя. Фото есть не у всех и может быть закрыто настройками приватности —
 * тогда остаются инициалы.
 */
export default function Avatar({
  id,
  name,
  other,
  partner,
  size = 24,
}: {
  id: number;
  name: string;
  other?: string;
  partner?: boolean;
  size?: number;
}) {
  const [failed, setFailed] = useState(false);

  return (
    <span
      className={`who${partner ? " who--partner" : ""}`}
      style={{ width: size, height: size, fontSize: size <= 24 ? 11 : 13 }}
      title={name}
    >
      {failed ? (
        initials(name, other)
      ) : (
        <img
          className="who__img"
          src={`/api/avatar/${id}`}
          alt=""
          onError={() => setFailed(true)}
        />
      )}
    </span>
  );
}
