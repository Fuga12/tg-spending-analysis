import { useRef, useState } from "react";
import { api, Me } from "./api";
import Avatar from "./Avatar";

/** Сторона готовой картинки. Кружок рисуется 24 пикселями, но экраны бывают
 *  втрое плотнее, а на этом экране фото показывается ещё и крупно. */
const SIDE = 256;

/**
 * Своё фото профиля.
 *
 * Telegram отдаёт фото не всем: у кого-то его нет вовсе, у кого-то оно
 * закрыто настройками приватности — и тогда в списке остаются инициалы.
 * Поставленное здесь фото телеграмным не перебивается никогда.
 *
 * Обрезает и жмёт браузер: гонять на сервер восьмимегабайтный снимок с
 * телефона ради кружка в 24 пикселя незачем.
 */
export default function Photo({
  me,
  onClose,
  onSaved,
}: {
  me: Me | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [preview, setPreview] = useState<string | null>(null);
  const [reading, setReading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const file = useRef<HTMLInputElement>(null);

  // Номер попытки: снимок с телефона разбирается секунду-другую, и если за это
  // время выбрать другой файл, ответы вернутся не по порядку — на экране
  // окажется не то фото, которое выбрали последним.
  const attempt = useRef(0);

  async function pick(chosen: File) {
    const id = ++attempt.current;
    setError(null);
    setReading(true);
    try {
      const shot = await square(chosen);
      if (id === attempt.current) setPreview(shot);
    } catch {
      if (id !== attempt.current) return;
      // Прошлый предпросмотр убираем: иначе рядом с ошибкой осталась бы
      // прошлая картинка, и «Сохранить» сохранило бы её вместо выбранной.
      setPreview(null);
      setError("не смог прочитать эту картинку");
    } finally {
      if (id === attempt.current) setReading(false);
    }
  }

  async function run(action: () => Promise<unknown>) {
    setBusy(true);
    setError(null);
    try {
      await action();
      onSaved();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "не сохранилось");
      setBusy(false);
    }
  }

  // Пока идёт запрос, лист не закрывается: закрытый лист унёс бы с собой
  // сообщение об ошибке, и человек решил бы, что фото сохранилось.
  const close = () => {
    if (!busy) onClose();
  };

  return (
    <div className="backdrop" onClick={(e) => e.target === e.currentTarget && close()}>
      <div className="sheet" role="dialog" aria-modal="true">
        <div className="sheet__grip" />
        <h2 className="sheet__title">Фото профиля</h2>

        <div className="photo">
          {preview ? (
            <img className="photo__preview" src={preview} alt="Как будет выглядеть" />
          ) : (
            me && (
              <Avatar
                id={me.id}
                name={me.name}
                other={me.partner?.name}
                version={me.avatar_version}
                size={96}
              />
            )
          )}
        </div>

        <p className="sheet__hint">
          Telegram отдаёт фото не всем: у кого-то его нет, у кого-то оно закрыто
          настройками приватности. Это фото телеграмным не заменится — оно
          останется, пока не убрать его руками.
        </p>

        <input
          ref={file}
          type="file"
          accept="image/*"
          hidden
          onChange={(e) => {
            const chosen = e.target.files?.[0];
            // Сброс значения: без него выбор того же файла второй раз
            // (например, после ошибки) не вызывает onChange вовсе.
            e.target.value = "";
            if (chosen) void pick(chosen);
          }}
        />

        {error && <div className="sheet__error">{error}</div>}

        {preview ? (
          <>
            <button
              className="btn"
              disabled={busy || reading}
              onClick={() => void run(() => api.setAvatar(preview))}
            >
              {busy ? "Сохраняю…" : "Сохранить"}
            </button>
            <button
              className="btn btn--text"
              disabled={busy || reading}
              onClick={() => file.current?.click()}
            >
              {reading ? "Читаю…" : "Выбрать другое"}
            </button>
          </>
        ) : (
          <>
            <button
              className="btn"
              disabled={busy || reading}
              onClick={() => file.current?.click()}
            >
              {reading ? "Читаю…" : "Выбрать фото"}
            </button>
            {me?.avatar_version && (
              <button
                className="btn btn--text btn--danger"
                disabled={busy || reading}
                onClick={() => void run(api.clearAvatar)}
              >
                Убрать фото
              </button>
            )}
          </>
        )}

        <button className="btn btn--text" disabled={busy} onClick={close}>
          Закрыть
        </button>
      </div>
    </div>
  );
}

/** Центральный квадрат, сжатый до SIDE. Полноценный кроп-редактор ради
 *  аватарки в кружке — отдельный экран, который никто не откроет второй раз. */
async function square(file: File): Promise<string> {
  const image = await load(file);
  try {
    const side = Math.min(image.width, image.height);
    if (!side) throw new Error("пустая картинка");

    const canvas = document.createElement("canvas");
    canvas.width = SIDE;
    canvas.height = SIDE;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("нет canvas");

    // JPEG прозрачности не знает и подставляет вместо неё чёрный. Без заливки
    // логотип или вырезанный портрет с прозрачным фоном приезжают лицом на
    // чёрном квадрате.
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, SIDE, SIDE);

    ctx.drawImage(
      image,
      (image.width - side) / 2,
      (image.height - side) / 2,
      side,
      side,
      0,
      0,
      SIDE,
      SIDE,
    );
    return canvas.toDataURL("image/jpeg", 0.85);
  } finally {
    // Развёрнутый снимок с телефона — это десятки мегабайт мимо кучи JS,
    // и сборщик про них не знает: три примерки подряд роняют вкладку.
    if ("close" in image) image.close();
  }
}

/** createImageBitmap разворачивает снимок по EXIF — без этого фото с телефона
 *  приезжает боком. Формат он берёт не всякий, поэтому есть запасной путь. */
async function load(file: File): Promise<ImageBitmap | HTMLImageElement> {
  if (typeof createImageBitmap === "function") {
    try {
      return await createImageBitmap(file, { imageOrientation: "from-image" });
    } catch {
      // не осилил — пробуем через <img>
    }
  }

  const url = URL.createObjectURL(file);
  try {
    const img = new Image();
    await new Promise((done, fail) => {
      img.onload = done;
      img.onerror = () => fail(new Error("не картинка"));
      img.src = url;
    });
    return img;
  } finally {
    URL.revokeObjectURL(url);
  }
}
