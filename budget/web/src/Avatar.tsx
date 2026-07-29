import { useEffect, useState } from "react";
import { initials } from "./format";

/**
 * Фото профиля. Раздаёт наш сервер: отдавать браузеру токен бота нельзя.
 * Своё, поставленное на сайте, побеждает телеграмное; если нет ни того ни
 * другого — остаются инициалы.
 */
export default function Avatar({
  id,
  name,
  other,
  partner,
  version,
  size = 24,
}: {
  id: number;
  name: string;
  other?: string;
  partner?: boolean;
  /** Метка своего фото. Меняется при замене — иначе браузер отдаёт старое. */
  version?: string;
  size?: number;
}) {
  const [failed, setFailed] = useState(false);

  // Поставили фото — версия сменилась, и прошлый промах больше не в счёт:
  // без сброса на экране остались бы инициалы до перезагрузки страницы.
  useEffect(() => setFailed(false), [id, version]);

  return (
    <span
      className={`who${partner ? " who--partner" : ""}`}
      style={{ width: size, height: size, fontSize: Math.max(11, Math.round(size * 0.42)) }}
      title={name}
    >
      {failed ? (
        initials(name, other)
      ) : (
        <img
          className="who__img"
          src={`/api/avatar/${id}${version ? `?v=${version}` : ""}`}
          alt=""
          onError={() => setFailed(true)}
        />
      )}
    </span>
  );
}
