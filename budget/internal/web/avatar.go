package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // фото с компьютера чаще всего PNG — иначе оно не разберётся
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Аватары берём у Telegram и раздаём со своего сервера: иначе токен бота
// пришлось бы отдать браузеру. Кэш в памяти — картинки маленькие, а ходить
// в Telegram на каждую строку списка незачем.
const (
	avatarTTL     = 12 * time.Hour
	avatarMaxSize = 1 << 20
	avatarTimeout = 8 * time.Second
)

// Границы своего фото. Обрезает и жмёт браузер, сюда приезжает уже готовый
// квадрат — потолки нужны на случай запроса, собранного руками.
const (
	avatarSideMax   = 512       // пикселей: кружок рисуется 24, больше незачем
	avatarStoredMax = 256 << 10 // байт готовой картинки
	avatarBodyMax   = 640 << 10 // base64 распухает на треть, плюс обвязка JSON
	avatarQuality   = 85
)

type avatar struct {
	data      []byte
	etag      string
	fetchedAt time.Time
	missing   bool // фото нет или закрыто настройками приватности
}

type avatarCache struct {
	mu     sync.Mutex
	byUser map[int64]avatar
}

func newAvatarCache() *avatarCache {
	return &avatarCache{byUser: map[int64]avatar{}}
}

// handleAvatar отдаёт фото профиля участника бюджета.
func (s *Server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "не понял, чей это аватар")
		return
	}
	// Чужие фото через наш сервер не раздаём — только двоих своих.
	if !s.cfg.IsAllowed(id) {
		writeError(w, http.StatusNotFound, "нет такого участника")
		return
	}

	// Своё фото побеждает телеграмное всегда и не перепроверяется: человек
	// поставил его руками, и подменять его тем, что вернул Telegram, нельзя.
	own, err := s.store.Avatar(r.Context(), id)
	if err != nil {
		s.log.Error("чтение своего аватара", "err", err, "user_id", id)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	if own != nil {
		sum := sha256.Sum256(own)
		// Надолго кэшируется только адрес с версией: она меняется вместе с
		// фото, поэтому новое приезжает под новым адресом. На адрес без версии
		// (партнёр открыл вкладку раньше, чем мы поставили фото, — в его
		// /api/me версии ещё нет) долгий кэш ставить нельзя: убранное фото
		// осталось бы у него на год, и отозвать его было бы нечем.
		cache := "private, no-cache"
		if r.URL.Query().Get("v") != "" {
			cache = "private, max-age=31536000, immutable"
		}
		writeImage(w, r, own, `"`+hex.EncodeToString(sum[:16])+`"`, cache)
		return
	}

	pic, err := s.avatars.get(r.Context(), s.cfg.BotToken, id)
	if err != nil {
		s.log.Warn("аватар", "err", err, "user_id", id)
		writeError(w, http.StatusNotFound, "аватар недоступен")
		return
	}
	if pic.missing {
		// Фронт нарисует инициалы — это не ошибка, а обычное дело.
		writeError(w, http.StatusNotFound, "аватара нет")
		return
	}
	writeImage(w, r, pic.data, pic.etag, "private, max-age=43200")
}

func writeImage(w http.ResponseWriter, r *http.Request, data []byte, etag, cache string) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", cache)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// avatarBody — тело PUT: картинка в base64, как её отдаёт canvas на фронте.
// Обычным JSON, а не multipart: под /api всё остальное тоже JSON, а тридцать
// килобайт лишней трети на base64 никого здесь не разорят.
type avatarBody struct {
	Photo string `json:"photo"`
}

// handleAvatarSet ставит своё фото профиля. Только себе: аватарка партнёра —
// его дело, и менять её за него незачем.
func (s *Server) handleAvatarSet(w http.ResponseWriter, r *http.Request) {
	var body avatarBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, avatarBodyMax)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "фото слишком тяжёлое или запрос не разобрался")
		return
	}

	pic, err := decodeAvatar(body.Photo)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	me := userID(r)
	at, err := s.store.SetAvatar(r.Context(), me, pic)
	if err != nil {
		s.log.Error("сохранение своего аватара", "err", err, "user_id", me)
		writeError(w, http.StatusInternalServerError, "не смог сохранить")
		return
	}
	s.log.Info("своё фото профиля поставлено", "user_id", me, "байт", len(pic))
	writeJSON(w, http.StatusOK, map[string]string{"avatar_version": avatarVersion(&at)})
}

// handleAvatarClear убирает своё фото: аватарка возвращается к телеграмной,
// а если её нет — к инициалам.
func (s *Server) handleAvatarClear(w http.ResponseWriter, r *http.Request) {
	me := userID(r)
	if err := s.store.ClearAvatar(r.Context(), me); err != nil {
		s.log.Error("снятие своего аватара", "err", err, "user_id", me)
		writeError(w, http.StatusInternalServerError, "не смог убрать")
		return
	}
	s.log.Info("своё фото профиля убрано", "user_id", me)
	writeJSON(w, http.StatusOK, map[string]string{"avatar_version": ""})
}

// avatarVersion — метка фото для URL картинки. Без неё браузер полсуток
// показывал бы из кэша старое фото по тому же адресу.
func avatarVersion(at *time.Time) string {
	if at == nil {
		return ""
	}
	return strconv.FormatInt(at.UnixMilli(), 10)
}

// decodeAvatar проверяет присланное и перекодирует в JPEG.
//
// Перекодирование — не косметика. Оно подтверждает, что это картинка, а не
// что угодно с подходящим расширением, и выбрасывает метаданные: в снимке с
// телефона лежат координаты места съёмки, и хранить их в базе бюджета незачем.
func decodeAvatar(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	// «data:image/jpeg;base64,...» — префикс от canvas на фронте.
	if strings.HasPrefix(raw, "data:") {
		if i := strings.Index(raw, ","); i > 0 {
			raw = raw[i+1:]
		}
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(data) == 0 {
		return nil, errors.New("не разобрал картинку")
	}

	// Размеры читаются из заголовка, до полного разбора: картинка 30000×30000
	// весит килобайты, а в память разворачивается гигабайтами и кладёт процесс
	// вместе с ботом.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("это не картинка — нужен JPEG или PNG")
	}
	if cfg.Width > avatarSideMax || cfg.Height > avatarSideMax {
		return nil, fmt.Errorf("картинка больше %d пикселей по стороне", avatarSideMax)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("не разобрал картинку")
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: avatarQuality}); err != nil {
		return nil, errors.New("не смог пережать картинку")
	}
	if out.Len() > avatarStoredMax {
		return nil, errors.New("фото слишком тяжёлое")
	}
	return out.Bytes(), nil
}

func (c *avatarCache) get(ctx context.Context, token string, userID int64) (avatar, error) {
	c.mu.Lock()
	cached, ok := c.byUser[userID]
	c.mu.Unlock()
	if ok && time.Since(cached.fetchedAt) < avatarTTL {
		return cached, nil
	}

	pic, err := fetchAvatar(ctx, token, userID)
	if err != nil {
		if ok {
			// Telegram не ответил — отдаём то, что было.
			return cached, nil
		}
		return avatar{}, err
	}

	c.mu.Lock()
	c.byUser[userID] = pic
	c.mu.Unlock()
	return pic, nil
}

// fetchAvatar тянет фото профиля: сначала список, потом файл.
func fetchAvatar(ctx context.Context, token string, userID int64) (avatar, error) {
	if token == "" {
		// Режим «только веб»: токена бота нет, значит и аватаров нет.
		return avatar{missing: true, fetchedAt: time.Now()}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, avatarTimeout)
	defer cancel()

	var photos struct {
		OK     bool `json:"ok"`
		Result struct {
			TotalCount int `json:"total_count"`
			Photos     [][]struct {
				FileID       string `json:"file_id"`
				FileUniqueID string `json:"file_unique_id"`
				Width        int    `json:"width"`
			} `json:"photos"`
		} `json:"result"`
	}
	if err := telegramCall(ctx, token,
		fmt.Sprintf("getUserProfilePhotos?user_id=%d&limit=1", userID), &photos); err != nil {
		return avatar{}, err
	}
	if !photos.OK || photos.Result.TotalCount == 0 || len(photos.Result.Photos) == 0 {
		return avatar{missing: true, fetchedAt: time.Now()}, nil
	}

	// Берём размер поменьше: в списке аватар 24 пикселя, тащить 640 незачем.
	sizes := photos.Result.Photos[0]
	pick := sizes[0]
	for _, s := range sizes {
		if s.Width >= 160 && s.Width < pick.Width || pick.Width < 160 && s.Width > pick.Width {
			pick = s
		}
	}

	var file struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := telegramCall(ctx, token, "getFile?file_id="+pick.FileID, &file); err != nil {
		return avatar{}, err
	}
	if !file.OK || file.Result.FilePath == "" {
		return avatar{missing: true, fetchedAt: time.Now()}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.telegram.org/file/bot"+token+"/"+file.Result.FilePath, nil)
	if err != nil {
		return avatar{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return avatar{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return avatar{}, fmt.Errorf("telegram отдал файл с кодом %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, avatarMaxSize))
	if err != nil {
		return avatar{}, err
	}
	return avatar{
		data:      data,
		etag:      `"` + pick.FileUniqueID + `"`,
		fetchedAt: time.Now(),
	}, nil
}

func telegramCall(ctx context.Context, token, method string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.telegram.org/bot"+token+"/"+method, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return json.NewDecoder(io.LimitReader(resp.Body, avatarMaxSize)).Decode(out)
}
