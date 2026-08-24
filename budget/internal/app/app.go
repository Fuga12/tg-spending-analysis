package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"budget/internal/config"
	"budget/internal/group"
	"budget/internal/storage"
)

// requestTimeout — потолок на один запрос к API. Больше здесь нечему длиться:
// в модель ходит бот, а не приложение.
const requestTimeout = 10 * time.Second

// Server — Mini App: API поверх хранилища и статика собранного фронта.
type Server struct {
	cfg    *config.Config
	store  *storage.Store
	groups *group.Service
	log    *slog.Logger
	http   *http.Server
}

// New собирает сервер приложения.
//
// Про dev-байпас кричим прямо здесь: он снимает единственную проверку,
// которая вообще есть, и уехать в прод молча не должен. Строчка в логе —
// последнее, что отделяет «удобно разрабатывать» от «заходи кто хочешь».
func New(cfg *config.Config, store *storage.Store, groups *group.Service, log *slog.Logger) *Server {
	if cfg.DevBypass() {
		log.Warn("ВНИМАНИЕ: APP_DEV_USER_ID задан — приложение пускает без подписи Telegram",
			"user_id", cfg.AppDevUserID,
			"это", "режим разработки, в проде переменной быть не должно")
	}

	s := &Server{cfg: cfg, store: store, groups: groups, log: log}
	s.http = &http.Server{
		Addr:              cfg.AppListen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Start слушает, пока не позовут Shutdown.
func (s *Server) Start() error {
	s.log.Info("приложение слушает", "addr", s.cfg.AppListen, "url", s.cfg.AppURL)
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown дожидается начатых запросов.
func (s *Server) Shutdown(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.http.Shutdown(ctx); err != nil {
		s.log.Warn("приложение не остановилось за отведённое время", "err", err)
	}
}

// caller — тот, кто пришёл: уже заведённый пользователь и, если он в группе,
// его участие вместе со скоупнутым хранилищем.
type caller struct {
	user    storage.User
	member  storage.Member
	inGroup bool
	group   *storage.GroupStore
}

// ctxKey — ключ вызывающего в контексте запроса. Свой тип, чтобы не
// столкнуться с чужими ключами.
type ctxKey struct{}

func callerFrom(r *http.Request) *caller { return r.Context().Value(ctxKey{}).(*caller) }

// authenticate — единственная дверь внутрь.
//
// Проверка подписи стоит перед всем: ни одного маршрута, который её обходит,
// нет и быть не должно — браузерной ветки не существует, и обходная дверь
// сразу стала бы единственной, которой пользуются.
func (s *Server) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		tg, err := s.identify(r)
		if err != nil {
			s.log.Warn("не пустил в приложение", "err", err, "addr", r.RemoteAddr)
			fail(w, http.StatusUnauthorized, "Открой приложение из Telegram.")
			return
		}
		if !s.cfg.IsAllowed(tg.ID) {
			fail(w, http.StatusForbidden, "Бот приватный.")
			return
		}

		if err := s.store.EnsureUser(ctx, tg.ID, tg.DisplayName()); err != nil {
			s.log.Error("запись пользователя", "err", err, "user_id", tg.ID)
			fail(w, http.StatusServiceUnavailable, "База не отвечает.")
			return
		}
		user, err := s.store.User(ctx, tg.ID)
		if err != nil {
			s.log.Error("чтение пользователя", "err", err, "user_id", tg.ID)
			fail(w, http.StatusServiceUnavailable, "База не отвечает.")
			return
		}

		c := &caller{user: user}
		c.member, c.inGroup, err = s.store.MemberOf(ctx, tg.ID)
		if err != nil {
			s.log.Error("поиск группы", "err", err, "user_id", tg.ID)
			fail(w, http.StatusServiceUnavailable, "База не отвечает.")
			return
		}
		if c.inGroup {
			c.group = s.store.ForGroup(c.member.GroupID)
		}

		next(w, r.WithContext(context.WithValue(ctx, ctxKey{}, c)))
	}
}

// identify достаёт открывшего из подписи Telegram или из dev-байпаса.
func (s *Server) identify(r *http.Request) (TelegramUser, error) {
	initData := initDataFrom(r)

	// Байпас работает, только когда подписи нет вовсе: иначе достаточно
	// прислать мусор вместо подписи, чтобы проверка отключилась.
	if initData == "" && s.cfg.DevBypass() {
		return TelegramUser{ID: s.cfg.AppDevUserID, FirstName: "Разработчик"}, nil
	}
	return VerifyInitData(initData, s.cfg.BotToken, time.Now())
}

// initDataFrom читает подпись из заголовка «Authorization: tma <initData>» —
// так это делает официальный клиент. Query-параметр не поддерживается
// намеренно: подпись оказалась бы в логах прокси и в истории браузера.
func initDataFrom(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if rest, ok := strings.CutPrefix(auth, "tma "); ok {
		return strings.TrimSpace(rest)
	}
	return ""
}

// ok отдаёт успешный ответ.
func ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if v == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Заголовки уже ушли — сказать клиенту больше нечего.
		slog.Error("не отдал ответ", "err", err)
	}
}

// fail отвечает ошибкой в том виде, в каком её можно показать человеку.
// Подробности остаются в логе: клиенту от «pq: connection refused» толку нет,
// а вот подсказки о внутреннем устройстве в нём есть.
func fail(w http.ResponseWriter, code int, text string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": text})
}

// decode читает тело запроса с потолком на размер.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "Не понял запрос.")
		return false
	}
	return true
}
