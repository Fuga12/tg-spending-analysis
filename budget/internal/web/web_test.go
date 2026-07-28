package web

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"budget/internal/config"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// freePort просит у системы любой свободный порт.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("порт: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func testServer(t *testing.T) (*Server, string) {
	t.Helper()

	addr := freePort(t)
	cfg := &config.Config{WebAddr: addr, WebBaseURL: "http://" + addr, WebInsecureCookies: true}

	s, err := New(cfg, nil, quietLog())
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("старт: %v", err)
	}
	t.Cleanup(s.Shutdown)

	waitReady(t, "http://"+addr+"/api/health")
	return s, "http://" + addr
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("сервер не поднялся за три секунды")
}

func TestHealth(t *testing.T) {
	_, base := testServer(t)

	resp, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("код = %d, ожидался 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("тело = %q", body)
	}
}

func TestServesStatic(t *testing.T) {
	_, base := testServer(t)

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, ожидался 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<title>Бюджет</title>") {
		t.Errorf("отдана не та страница: %.200s", body)
	}
}

func TestBusyPortIsStartupError(t *testing.T) {
	// Занятый порт — ошибка старта: сервис, который работает наполовину
	// и молчит об этом, хуже упавшего (webapp.md §3).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("порт: %v", err)
	}
	defer ln.Close()

	cfg := &config.Config{WebAddr: ln.Addr().String(), WebBaseURL: "http://x", WebInsecureCookies: true}
	s, err := New(cfg, nil, quietLog())
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	if err := s.Start(); err == nil {
		s.Shutdown()
		t.Error("ожидалась ошибка занятого порта")
	}
}

func TestShutdownStopsListening(t *testing.T) {
	s, base := testServer(t)
	s.Shutdown()

	client := &http.Client{Timeout: time.Second}
	if resp, err := client.Get(base + "/api/health"); err == nil {
		resp.Body.Close()
		t.Error("после остановки сервер не должен отвечать")
	}
}

func TestPanicDoesNotKillServer(t *testing.T) {
	// Паника в обработчике не должна ронять процесс вместе с ботом.
	addr := freePort(t)
	cfg := &config.Config{WebAddr: addr, WebBaseURL: "http://" + addr, WebInsecureCookies: true}
	s, err := New(cfg, nil, quietLog())
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("тест") })
	mux.HandleFunc("GET /api/health", s.health)
	s.http.Handler = s.recoverPanic(mux)

	if err := s.Start(); err != nil {
		t.Fatalf("старт: %v", err)
	}
	t.Cleanup(s.Shutdown)
	waitReady(t, "http://"+addr+"/api/health")

	resp, err := http.Get("http://" + addr + "/boom")
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("код = %d, ожидался 500", resp.StatusCode)
	}

	// И сервер продолжает работать.
	after, err := http.Get("http://" + addr + "/api/health")
	if err != nil {
		t.Fatalf("сервер умер после паники: %v", err)
	}
	after.Body.Close()
}
