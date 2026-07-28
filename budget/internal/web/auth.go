package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"budget/internal/auth"
	"budget/internal/storage"
)

// userKey — ключ идентификатора пользователя в контексте запроса.
type userKey struct{}

// authAttemptsPerMinute — потолок попыток обмена ссылки с одного адреса.
// Токен угадать нельзя, но долбиться в /auth тоже незачем (webapp.md §1).
const authAttemptsPerMinute = 10

// handleAuth обменивает одноразовую ссылку на сессию.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientIP(r)) {
		http.Error(w, "слишком много попыток входа, подожди минуту", http.StatusTooManyRequests)
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		s.loginFailed(w, "ссылка без токена")
		return
	}

	userID, err := s.store.ConsumeLoginToken(r.Context(), auth.Hash(token))
	if err != nil {
		if errors.Is(err, storage.ErrNoSession) {
			// Ссылка одноразовая и живёт пять минут — это нормальный исход,
			// а не поломка.
			s.loginFailed(w, "ссылка уже использована или устарела")
			return
		}
		s.log.Error("обмен ссылки входа", "err", err)
		s.loginFailed(w, "база не отвечает")
		return
	}

	// Whitelist проверяется и здесь, и на каждом запросе: id могли убрать из
	// ALLOWED_USER_IDS уже после выдачи ссылки.
	if !s.cfg.IsAllowed(userID) {
		s.log.Warn("вход по ссылке от того, кого нет в whitelist", "user_id", userID)
		s.loginFailed(w, "доступ закрыт")
		return
	}

	sessionToken, hash, err := auth.NewToken()
	if err != nil {
		s.log.Error("токен сессии", "err", err)
		s.loginFailed(w, "не смог завести сессию")
		return
	}
	if err := s.store.CreateSession(r.Context(), hash, userID, auth.SessionTTL, r.UserAgent()); err != nil {
		s.log.Error("создание сессии", "err", err)
		s.loginFailed(w, "не смог завести сессию")
		return
	}

	// Протухшее убираем здесь же: пары строк в неделю не стоят отдельного крона.
	if err := s.store.CleanupAuth(r.Context()); err != nil {
		s.log.Warn("чистка протухших сессий", "err", err)
	}

	http.SetCookie(w, s.sessionCookie(sessionToken, auth.SessionTTL))
	s.log.Info("вход", "user_id", userID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// loginFailed отвечает страницей, а не JSON: сюда приходят по ссылке из чата,
// и человек должен прочитать, что делать дальше.
func (s *Server) loginFailed(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Бюджет</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;
font:16px/1.5 system-ui,sans-serif;background:#0d0d0d;color:#fff;text-align:center}
p{color:#c3c2b7;max-width:28em;padding:0 24px}</style>
<main><h1>Не пустил</h1><p>` + reason + `.</p>
<p>Напиши боту <b>/вход</b> — он пришлёт новую ссылку.</p></main>`))
}

// sessionCookie собирает cookie сессии. Флаг Secure выключается только явно
// заданным WEB_INSECURE_COOKIES: иначе браузер по HTTP выбросит cookie, и
// вход будет молча не работать (webapp.md §1).
func (s *Server) sessionCookie(token string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   !s.cfg.WebInsecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	}
}

// requireSession пускает дальше только с живой сессией и только тех, кто
// сейчас в whitelist.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "нужен вход")
			return
		}

		hash := auth.Hash(cookie.Value)
		userID, err := s.store.SessionUser(r.Context(), hash)
		if err != nil {
			if !errors.Is(err, storage.ErrNoSession) {
				s.log.Error("чтение сессии", "err", err)
			}
			s.clearCookie(w)
			writeError(w, http.StatusUnauthorized, "сессия закончилась")
			return
		}
		// Убрали id из ALLOWED_USER_IDS — сессия мертва сразу, а не через месяц.
		if !s.cfg.IsAllowed(userID) {
			s.log.Warn("сессия пользователя вне whitelist", "user_id", userID)
			_ = s.store.DeleteUserSessions(r.Context(), userID)
			s.clearCookie(w)
			writeError(w, http.StatusUnauthorized, "доступ закрыт")
			return
		}

		if err := s.store.TouchSession(r.Context(), hash); err != nil {
			s.log.Warn("отметка сессии", "err", err)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, userID)))
	})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	c := s.sessionCookie("", 0)
	c.MaxAge = -1
	http.SetCookie(w, c)
}

// userID достаёт пользователя, положенного requireSession.
func userID(r *http.Request) int64 {
	id, _ := r.Context().Value(userKey{}).(int64)
	return id
}

// handleLogout — выход с этого устройства или отовсюду.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("all") == "1" {
		if err := s.store.DeleteUserSessions(r.Context(), userID(r)); err != nil {
			s.log.Error("выход отовсюду", "err", err)
			writeError(w, http.StatusInternalServerError, "не смог выйти")
			return
		}
	} else if cookie, err := r.Cookie(auth.CookieName); err == nil {
		if err := s.store.DeleteSession(r.Context(), auth.Hash(cookie.Value)); err != nil {
			s.log.Error("выход", "err", err)
			writeError(w, http.StatusInternalServerError, "не смог выйти")
			return
		}
	}
	s.clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// rateLimiter — счётчик попыток на адрес в минутном окне. Для двух
// пользователей этого достаточно, внешних зависимостей не нужно.
type rateLimiter struct {
	limit int

	mu      sync.Mutex
	windows map[string]*window
	now     func() time.Time
}

type window struct {
	started time.Time
	count   int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{limit: limit, windows: map[string]*window{}, now: time.Now}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	w, ok := l.windows[key]
	if !ok || now.Sub(w.started) >= time.Minute {
		// Заодно подчищаем чужие протухшие окна, чтобы карта не росла вечно.
		for k, old := range l.windows {
			if now.Sub(old.started) >= time.Minute {
				delete(l.windows, k)
			}
		}
		l.windows[key] = &window{started: now, count: 1}
		return true
	}
	w.count++
	return w.count <= l.limit
}

// clientIP — адрес без порта. За прокси адрес будет прокси; для домашнего
// сервиса это приемлемо, а доверять X-Forwarded-For без настройки прокси
// нельзя: заголовок подделывается тривиально.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
