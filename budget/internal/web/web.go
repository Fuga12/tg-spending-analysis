// Package web — веб-интерфейс к тем же данным, что у бота: правки и графики.
//
// Живёт в том же процессе и на той же базе. К модели не обращается никогда:
// разбор текста — дело бота, веб только читает и правит уже записанное
// (webapp.md §0).
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
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
	cfg   *config.Config
	store *storage.Store
	log   *slog.Logger
	http  *http.Server
}

// New собирает сервер. Ошибка означает, что запускаться нельзя.
func New(cfg *config.Config, store *storage.Store, log *slog.Logger) (*Server, error) {
	static, err := staticFS()
	if err != nil {
		return nil, err
	}

	s := &Server{cfg: cfg, store: store, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.Handle("GET /", http.FileServer(http.FS(static)))

	s.http = &http.Server{
		Addr:         cfg.WebAddr,
		Handler:      s.recoverPanic(s.logRequest(mux)),
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
			if rec := recover(); rec != nil {
				s.log.Error("паника в обработчике", "panic", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "что-то сломалось")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

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

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("не отдал ответ", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// staticFS отдаёт собранный фронт из бинаря.
func staticFS() (fs.FS, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("статика фронта: %w", err)
	}
	return sub, nil
}
