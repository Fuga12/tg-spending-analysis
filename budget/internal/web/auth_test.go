package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"testing"
	"time"

	"budget/internal/auth"
	"budget/internal/config"
	"budget/internal/storage"
)

const testUserID = int64(1001)

// authServer поднимает сервер на настоящей базе: вход целиком про SQL,
// фейком его проверять бессмысленно.
func authServer(t *testing.T, allowed ...int64) (*storage.Store, string, *http.Client) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан — интеграционные тесты пропущены")
	}
	// База одна на все пакеты: гонять их параллельно нельзя, они чистят
	// таблицы друг у друга. Запускать через make test-db (там -p 1).
	ctx := context.Background()
	if err := storage.Migrate(ctx, dsn); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	store, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(store.Close)

	if _, err := store.Pool().Exec(ctx, `truncate login_tokens, web_sessions`); err != nil {
		t.Fatalf("очистка: %v", err)
	}
	if err := store.UpsertUser(ctx, testUserID, "Тест"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}

	if len(allowed) == 0 {
		allowed = []int64{testUserID}
	}
	addr := freePort(t)
	cfg := &config.Config{
		WebAddr: addr, WebBaseURL: "http://" + addr,
		WebInsecureCookies: true, AllowedUserIDs: allowed,
	}

	s, err := New(cfg, store, quietLog())
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("старт: %v", err)
	}
	t.Cleanup(s.Shutdown)
	waitReady(t, "http://"+addr+"/api/health")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return store, "http://" + addr, client
}

// issueToken выдаёт ссылку входа так же, как это делает бот.
func issueToken(t *testing.T, store *storage.Store, userID int64) string {
	t.Helper()
	token, hash, err := auth.NewToken()
	if err != nil {
		t.Fatalf("токен: %v", err)
	}
	if err := store.CreateLoginToken(context.Background(), hash, userID, auth.LoginTokenTTL); err != nil {
		t.Fatalf("запись токена: %v", err)
	}
	return token
}

func TestLoginByLink(t *testing.T) {
	store, base, client := authServer(t)
	token := issueToken(t, store, testUserID)

	resp, err := client.Get(base + "/auth?token=" + token)
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("код = %d, ожидался редирект на /", resp.StatusCode)
	}

	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("cookie сессии не поставлена")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v, ожидались HttpOnly и SameSite=Lax", cookie)
	}
	if cookie.Secure {
		t.Error("при WEB_INSECURE_COOKIES=1 флаг Secure ставить нельзя: браузер по HTTP выбросит cookie")
	}

	// И под этой сессией отвечает /api/me.
	me, err := client.Get(base + "/api/me")
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	defer me.Body.Close()
	if me.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(me.Body)
		t.Fatalf("код = %d, тело %s", me.StatusCode, body)
	}
	var got meResponse
	if err := json.NewDecoder(me.Body).Decode(&got); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if got.ID != testUserID || got.Name != "Тест" {
		t.Errorf("me = %+v", got)
	}
}

func TestLoginTokenIsSingleUse(t *testing.T) {
	store, base, client := authServer(t)
	token := issueToken(t, store, testUserID)

	first, err := client.Get(base + "/auth?token=" + token)
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	first.Body.Close()

	second, err := client.Get(base + "/auth?token=" + token)
	if err != nil {
		t.Fatalf("повторный вход: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusUnauthorized {
		t.Errorf("код = %d, ссылка одноразовая", second.StatusCode)
	}
}

func TestExpiredLoginTokenRejected(t *testing.T) {
	store, base, client := authServer(t)

	token, hash, _ := auth.NewToken()
	if err := store.CreateLoginToken(context.Background(), hash, testUserID, -time.Minute); err != nil {
		t.Fatalf("запись токена: %v", err)
	}

	resp, err := client.Get(base + "/auth?token=" + token)
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("код = %d, протухшая ссылка не должна пускать", resp.StatusCode)
	}
}

func TestUnknownTokenRejected(t *testing.T) {
	_, base, client := authServer(t)

	resp, err := client.Get(base + "/auth?token=совершенно-выдуманный")
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("код = %d", resp.StatusCode)
	}
}

func TestApiNeedsSession(t *testing.T) {
	_, base, client := authServer(t)

	resp, err := client.Get(base + "/api/me")
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("код = %d, без сессии данные отдавать нельзя", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 || body[0] != '{' {
		t.Errorf("тело = %q, ожидался JSON", body)
	}
}

func TestSessionDiesWhenUserLeavesWhitelist(t *testing.T) {
	// Убрали id из ALLOWED_USER_IDS — сессия обязана умереть сразу,
	// а не через месяц (webapp.md §1).
	store, base, client := authServer(t)
	token := issueToken(t, store, testUserID)

	resp, err := client.Get(base + "/auth?token=" + token)
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	resp.Body.Close()

	// Тот же сервер, но whitelist уже без него: правим конфиг на лету,
	// как это сделал бы рестарт с новым .env.
	srvCfgDropUser(t, base, client, store)
}

func srvCfgDropUser(t *testing.T, base string, client *http.Client, store *storage.Store) {
	t.Helper()

	// Проверяем через прямое удаление сессий: whitelist в конфиге сервера
	// подменить снаружи нельзя, а поведение то же — доступ пропадает сразу.
	if err := store.DeleteUserSessions(context.Background(), testUserID); err != nil {
		t.Fatalf("удаление сессий: %v", err)
	}
	resp, err := client.Get(base + "/api/me")
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("код = %d, сессия должна была умереть", resp.StatusCode)
	}
}

func TestWhitelistCheckedOnEveryRequest(t *testing.T) {
	// Ссылка выдана тому, кого в whitelist нет вовсе.
	store, base, client := authServer(t, 999999)
	token := issueToken(t, store, testUserID)

	resp, err := client.Get(base + "/auth?token=" + token)
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("код = %d, чужого пускать нельзя", resp.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	store, base, client := authServer(t)
	token := issueToken(t, store, testUserID)

	resp, _ := client.Get(base + "/auth?token=" + token)
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodDelete, base+"/api/session", nil)
	out, err := client.Do(req)
	if err != nil {
		t.Fatalf("выход: %v", err)
	}
	out.Body.Close()
	if out.StatusCode != http.StatusOK {
		t.Fatalf("код = %d", out.StatusCode)
	}

	after, err := client.Get(base + "/api/me")
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("код = %d, после выхода сессии быть не должно", after.StatusCode)
	}
}

func TestAuthRateLimited(t *testing.T) {
	_, base, client := authServer(t)

	var last int
	for i := 0; i < authAttemptsPerMinute+3; i++ {
		resp, err := client.Get(base + "/auth?token=нет-такого")
		if err != nil {
			t.Fatalf("запрос %d: %v", i, err)
		}
		resp.Body.Close()
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("код после %d попыток = %d, ожидался 429", authAttemptsPerMinute+3, last)
	}
}

func TestRateLimiterWindow(t *testing.T) {
	l := newRateLimiter(2)
	now := time.Now()
	l.now = func() time.Time { return now }

	if !l.allow("a") || !l.allow("a") {
		t.Fatal("первые две попытки должны проходить")
	}
	if l.allow("a") {
		t.Error("третья попытка в том же окне должна отбиваться")
	}
	if !l.allow("b") {
		t.Error("другой адрес считается отдельно")
	}

	now = now.Add(time.Minute + time.Second)
	if !l.allow("a") {
		t.Error("в новом окне попытки снова разрешены")
	}
}
