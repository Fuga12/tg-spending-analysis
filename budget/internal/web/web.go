// Package web — веб-интерфейс к тем же данным, что у бота: правки и графики.
//
// Живёт в том же процессе и на той же базе. К модели не обращается никогда:
// разбор текста — дело бота, веб только читает и правит уже записанное
// (webapp.md §0).
package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"budget/internal/config"
	"budget/internal/storage"
)

// Таймауты запросов. Веб не должен мешать боту записывать траты, поэтому
// зависший запрос обязан отваливаться сам.
const (
	readTimeout     = 10 * time.Second
	writeTimeout    = 30 * time.Second
	idleTimeout     = 60 * time.Second
	shutdownTimeout = 10 * time.Second
)

// Server — HTTP-интерфейс. Слушает адрес из WEB_ADDR.
type Server struct {
	cfg     *config.Config
	store   *storage.Store
	log     *slog.Logger
	http    *http.Server
	limiter *rateLimiter
	now     func() time.Time // подменяется в тестах
}

// New собирает сервер. Ошибка означает, что запускаться нельзя.
func New(cfg *config.Config, store *storage.Store, log *slog.Logger) (*Server, error) {
	// Без таймзоны сервер отвечал бы пятисотками на каждый запрос со
	// временем: падать на старте честнее.
	if cfg.TZ == nil {
		return nil, errors.New("веб: не задана таймзона")
	}

	static, err := staticFS()
	if err != nil {
		return nil, err
	}
	tags, err := etags(static)
	if err != nil {
		return nil, err
	}

	s := &Server{cfg: cfg, store: store, log: log, limiter: newRateLimiter(authAttemptsPerMinute), now: time.Now}

	// Под сессией — всё, что трогает данные. Health и статика открыты:
	// иначе страница не загрузится до входа.
	api := http.NewServeMux()
	api.HandleFunc("GET /api/me", s.handleMe)
	api.HandleFunc("DELETE /api/session", s.handleLogout)
	api.HandleFunc("GET /api/categories", s.handleCategories)
	api.HandleFunc("GET /api/transactions", s.handleTransactions)
	api.HandleFunc("GET /api/report/month", s.handleMonth)
	api.HandleFunc("GET /api/report/daily", s.handleDaily)
	api.HandleFunc("GET /api/report/months", s.handleMonths)
	api.HandleFunc("POST /api/transactions", s.handleCreate)
	api.HandleFunc("PATCH /api/transactions/{id}", s.handlePatch)
	api.HandleFunc("DELETE /api/transactions/{id}", s.handleDelete)
	api.HandleFunc("PATCH /api/categories/{id}", s.handleCategoryPatch)
	api.HandleFunc("POST /api/categories", s.handleCategoryCreate)
	// Свои заглушки на прочие методы: встроенный 405 у ServeMux — текстовый,
	// а под /api всё обязано быть JSON (webapp.md §4).
	api.HandleFunc("/api/me", methodNotAllowed)
	api.HandleFunc("/api/session", methodNotAllowed)
	api.HandleFunc("/api/categories", methodNotAllowed)
	api.HandleFunc("/api/transactions", methodNotAllowed)
	api.HandleFunc("/api/report/month", methodNotAllowed)
	api.HandleFunc("/api/report/daily", methodNotAllowed)
	api.HandleFunc("/api/report/months", methodNotAllowed)
	api.HandleFunc("/api/transactions/{id}", methodNotAllowed)
	api.HandleFunc("/api/categories/{id}", methodNotAllowed)
	api.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "нет такого метода")
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /auth", s.handleAuth)
	mux.HandleFunc("/auth", methodNotAllowed)
	for _, path := range []string{
		"/api/me", "/api/session", "/api/categories",
		"/api/transactions", "/api/transactions/", "/api/report/month",
		"/api/report/daily", "/api/report/months", "/api/categories/",
	} {
		mux.Handle(path, s.requireSession(api))
	}
	// Неизвестный /api/* обязан отвечать JSON-ошибкой, иначе фронт получит
	// текстовую страницу вместо {"error": ...} (webapp.md §4).
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "нет такого метода")
	})
	// Без метода в шаблоне: «GET /» конфликтует с «/api/» — ServeMux
	// считает такую пару неоднозначной и паникует при регистрации.
	mux.Handle("/", cacheStatic(tags, http.FileServer(http.FS(static))))

	s.http = &http.Server{
		Addr:         cfg.WebAddr,
		Handler:      s.logRequest(s.recoverPanic(mux)),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}
	return s, nil
}

// Start поднимает сервер. Занятый порт — ошибка старта: сервис, который
// работает наполовину и молчит об этом, хуже упавшего (webapp.md §3).
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.WebAddr)
	if err != nil {
		return fmt.Errorf("веб-сервер на %s: %w", s.cfg.WebAddr, err)
	}
	s.log.Info("веб поднят", "addr", s.cfg.WebAddr, "base_url", s.cfg.WebBaseURL)

	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("веб-сервер упал", "err", err)
		}
	}()
	return nil
}

// Shutdown даёт начатым запросам договорить.
func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(ctx); err != nil {
		s.log.Warn("веб-сервер не остановился по-хорошему", "err", err)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// recoverPanic — паника в обработчике не должна ронять бота вместе с вебом.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// Служебная паника net/http: обработчик просит тихо оборвать
			// соединение, это не ошибка и не наше дело.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			s.log.Error("паника в обработчике", "panic", rec, "path", r.URL.Path)

			// Если ответ уже пошёл клиенту, дописывать в него JSON-ошибку
			// поздно: получится склейка из половины ответа и половины ошибки.
			if sr, ok := w.(*statusRecorder); ok && sr.wrote {
				return
			}
			writeError(w, http.StatusInternalServerError, "что-то сломалось")
		}()
		next.ServeHTTP(w, r)
	})
}

// logRequest стоит снаружи recoverPanic: иначе паникнувшие запросы —
// самые интересные — в лог запросов не попадают вовсе.
func (s *Server) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		s.log.Info("запрос",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "за", time.Since(start).Round(time.Millisecond))
	})
}

// statusRecorder помнит код ответа и то, начали ли мы уже отвечать.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.status = code
	r.wrote = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("не отдал ответ", "err", err)
	}
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "метод "+r.Method+" здесь не работает")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// etags считает ETag каждого файла статики один раз при старте: у файлов из
// embed.FS нулевое время изменения, поэтому без этого браузер перекачивал бы
// бандл на каждое открытие страницы.
func etags(static fs.FS) (map[string]string, error) {
	tags := map[string]string{}

	err := fs.WalkDir(static, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(static, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		tags["/"+path] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("хэши статики: %w", err)
	}
	tags["/"] = tags["/index.html"]
	return tags, nil
}

// cacheStatic проставляет ETag и правила кэширования. Файлы с хэшем в имени
// (их выдаёт Vite) можно кэшировать вечно, index.html — нельзя никогда.
func cacheStatic(tags map[string]string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := tags[r.URL.Path]; ok {
			w.Header().Set("ETag", tag)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// staticFS отдаёт собранный фронт из бинаря.
func staticFS() (fs.FS, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("статика фронта: %w", err)
	}
	return sub, nil
}
