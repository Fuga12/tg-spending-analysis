package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"budget/internal/config"
	"budget/internal/storage"
)

// noPhoto убирает своё фото до и после теста. База одна на весь прогон, и
// оставленная картинка ломает соседний тест на следующем же запуске.
func noPhoto(t *testing.T, store *storage.Store) {
	t.Helper()
	drop := func() {
		if err := store.ClearAvatar(context.Background(), testUserID); err != nil {
			t.Fatalf("сброс фото: %v", err)
		}
	}
	drop()
	t.Cleanup(drop)
}

// pngPixels — картинка нужного размера в base64, как её присылает фронт.
func pngPixels(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("картинка: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// Своё фото побеждает телеграмное — ради этого всё и затевалось. Проверяется
// в обход HTTP: телеграмную картинку взять неоткуда (токена бота в тестах
// нет), поэтому она кладётся прямо в кэш, откуда её взял бы обработчик.
func TestOwnPhotoWinsOverTelegram(t *testing.T) {
	store, _, _ := loggedIn(t)
	noPhoto(t, store)

	addr := freePort(t)
	cfg := &config.Config{
		WebAddr: addr, WebBaseURL: "http://" + addr,
		WebInsecureCookies: true, AllowedUserIDs: []int64{testUserID},
		TZ: time.UTC,
	}
	s, err := New(cfg, store, quietLog())
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}

	const fromTelegram = "картинка из telegram"
	s.avatars.byUser[testUserID] = avatar{
		data: []byte(fromTelegram), etag: `"tg"`, fetchedAt: time.Now(),
	}

	own, err := decodeAvatar(pngPixels(t, 64, 64))
	if err != nil {
		t.Fatalf("картинка: %v", err)
	}
	if _, err := store.SetAvatar(context.Background(), testUserID, own); err != nil {
		t.Fatalf("сохранение: %v", err)
	}

	rec := httptest.NewRecorder()
	s.handleAvatar(rec, httptest.NewRequest(http.MethodGet,
		"/api/avatar/"+itoa(int(testUserID)), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код = %d, ожидался 200", rec.Code)
	}
	if got := rec.Body.String(); got == fromTelegram {
		t.Fatal("отдана телеграмная картинка — своё фото должно побеждать")
	} else if got != string(own) {
		t.Errorf("отдано не своё фото (%d байт вместо %d)", len(got), len(own))
	}
}

// Своё фото ставится и убирается через сайт: Telegram отдаёт фото не всем,
// а менять настройки приватности ради кружка в бюджете никто не станет.
func TestOwnPhotoRoundTrip(t *testing.T) {
	store, base, client := loggedIn(t)
	noPhoto(t, store)
	url := base + "/api/avatar/" + itoa(int(testUserID))

	// Токена бота в тестах нет — до своего фото аватарки нет вовсе.
	resp, _ := send(t, client, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("код = %d, без своего фото и без токена ожидался 404", resp.StatusCode)
	}

	resp, body := send(t, client, http.MethodPut, base+"/api/avatar",
		map[string]any{"photo": "data:image/png;base64," + pngPixels(t, 256, 256)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}
	var saved struct {
		Version string `json:"avatar_version"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if saved.Version == "" {
		t.Error("версия пустая — фронт не сбросит кэш картинки")
	}

	resp, body = send(t, client, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, своё фото должно отдаваться", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	// Хранится перекодированным: так подтверждается, что это картинка, и
	// выбрасываются метаданные снимка вроде координат съёмки.
	if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		t.Errorf("отдан не JPEG: %v", err)
	}
	// Адрес без версии кэшировать надолго нельзя: по нему ходит партнёр,
	// открывший вкладку до того, как фото поставили, — и убранное фото
	// осталось бы у него в кэше на год, отозвать было бы нечем.
	if cc := resp.Header.Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control без версии = %q — убранное фото залипнет в кэше", cc)
	}
	resp, _ = send(t, client, http.MethodGet, url+"?v="+saved.Version, nil)
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control с версией = %q — картинка перекачивается зря", cc)
	}

	// Версия в /api/me — по ней фронт строит адрес картинки.
	var me meResponse
	getJSON(t, client, base+"/api/me", &me)
	if me.AvatarVersion != saved.Version {
		t.Errorf("версия в /api/me = %q, ожидалась %q", me.AvatarVersion, saved.Version)
	}

	// Убрали — вернулись к телеграмной, а её нет.
	resp, body = send(t, client, http.MethodDelete, base+"/api/avatar", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}
	resp, _ = send(t, client, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("код = %d, после снятия фото ожидался 404", resp.StatusCode)
	}
	getJSON(t, client, base+"/api/me", &me)
	if me.AvatarVersion != "" {
		t.Errorf("версия = %q, после снятия ожидалась пустая", me.AvatarVersion)
	}
}

// Замена фото меняет версию: без этого браузер полсуток отдавал бы из кэша
// старую картинку по тому же адресу.
func TestNewPhotoChangesVersion(t *testing.T) {
	store, base, client := loggedIn(t)
	noPhoto(t, store)

	version := func() string {
		t.Helper()
		var me meResponse
		getJSON(t, client, base+"/api/me", &me)
		return me.AvatarVersion
	}

	resp, body := send(t, client, http.MethodPut, base+"/api/avatar",
		map[string]any{"photo": pngPixels(t, 64, 64)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}
	first := version()

	resp, body = send(t, client, http.MethodPut, base+"/api/avatar",
		map[string]any{"photo": pngPixels(t, 128, 128)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}
	if second := version(); second == first {
		t.Errorf("версия не изменилась (%q) — новое фото не доедет до браузера", second)
	}
}

// Картинка-бомба отвергается по заголовку, до разбора: 20000×20000 весит
// килобайты, а в память разворачивается гигабайтами и кладёт процесс вместе
// с ботом. Проверяется по тексту ошибки — он бывает только с этого пути.
func TestPhotoBombRejectedBeforeDecoding(t *testing.T) {
	_, err := decodeAvatar(hugePNG(t, 20000, 20000))
	if err == nil {
		t.Fatal("картинка-бомба принята")
	}
	if !strings.Contains(err.Error(), "пикселей по стороне") {
		t.Errorf("ошибка %q — размеры должны читаться из заголовка, до разбора", err)
	}
}

// hugePNG — заголовок настоящей картинки с подменёнными размерами. Рисовать
// её целиком нельзя: 20000×20000 — это те самые полтора гигабайта.
func hugePNG(t *testing.T, w, h uint32) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(pngPixels(t, 1, 1))
	if err != nil {
		t.Fatalf("картинка: %v", err)
	}
	// Раскладка PNG: 8 байт подписи, 4 длины, «IHDR», ширина, высота, дальше
	// пять байт свойств и контрольная сумма всего чанка вместе с именем.
	binary.BigEndian.PutUint32(data[16:20], w)
	binary.BigEndian.PutUint32(data[20:24], h)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	return base64.StdEncoding.EncodeToString(data)
}

// Всё, что не картинка, отвергается с 400, а не пятисоткой.
func TestPhotoValidation(t *testing.T) {
	store, base, client := loggedIn(t)
	noPhoto(t, store)

	cases := []struct {
		name  string
		photo string
	}{
		{"не картинка", base64.StdEncoding.EncodeToString([]byte("совсем не картинка"))},
		{"не base64", "%%%"},
		{"пусто", ""},
		{"больше потолка стороны", pngPixels(t, 700, 700)},
		{"картинка-бомба", hugePNG(t, 20000, 20000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := send(t, client, http.MethodPut, base+"/api/avatar",
				map[string]any{"photo": c.photo})
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("код = %d, ожидался 400 (тело %s)", resp.StatusCode, body)
			}
		})
	}
}

// Фото ставится только себе: у ручки нет и не должно быть параметра «чьё».
func TestPhotoIsOnlyYourOwn(t *testing.T) {
	store, base, client := loggedIn(t)
	noPhoto(t, store)
	// Свой id, ничей больше: 1002 занят фикстурой партнёра из write_test.go,
	// и удалять в конце чужую строку значило бы ломать соседний тест.
	const otherID = int64(1097)

	ctx := context.Background()
	if err := store.UpsertUser(ctx, otherID, "Посторонний"); err != nil {
		t.Fatalf("посторонний: %v", err)
	}
	t.Cleanup(func() {
		if _, err := store.Pool().Exec(ctx, `delete from users where id = $1`, otherID); err != nil {
			t.Errorf("уборка: %v", err)
		}
	})

	resp, body := send(t, client, http.MethodPut, base+"/api/avatar",
		map[string]any{"photo": pngPixels(t, 64, 64), "user_id": otherID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}

	// Фото поставилось — и поставилось себе, а не тому, кого назвали в теле.
	mine, err := store.Avatar(ctx, testUserID)
	if err != nil {
		t.Fatalf("чтение своего: %v", err)
	}
	if mine == nil {
		t.Fatal("своё фото не сохранилось — проверять дальше нечего")
	}
	data, err := store.Avatar(ctx, otherID)
	if err != nil {
		t.Fatalf("чтение чужого: %v", err)
	}
	if data != nil {
		t.Error("фото уехало постороннему — ставить можно только себе")
	}
}
