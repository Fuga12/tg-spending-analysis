package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", pic.etag)
	w.Header().Set("Cache-Control", "private, max-age=43200")
	if r.Header.Get("If-None-Match") == pic.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(pic.data)
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
