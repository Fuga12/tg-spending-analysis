package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/config"
	"budget/internal/group"
	"budget/internal/storage"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testServer поднимает приложение на настоящей базе и заводит группу из
// двоих плюс чужую группу рядом — без неё нечем проверить главное: что через
// HTTP не видно чужого.
type testServer struct {
	srv     *Server
	store   *storage.Store
	group   *storage.GroupStore
	members []storage.Member
	other   *storage.GroupStore
	otherM  storage.Member
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан — интеграционные тесты пропущены")
	}
	ctx := context.Background()
	if err := storage.Migrate(ctx, dsn); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	store, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(store.Close)

	if _, err := store.Pool().Exec(ctx, `
		truncate transactions, tx_recipients, word_map, llm_usage,
		         categories, invites, members, groups, users
		restart identity cascade`); err != nil {
		t.Fatalf("очистка: %v", err)
	}

	for id, name := range map[int64]string{1: "Илья", 2: "Аня", 9: "Чужой", 50: "Гость"} {
		if err := store.EnsureUser(ctx, id, name); err != nil {
			t.Fatalf("пользователь %d: %v", id, err)
		}
	}

	groups := group.New(store, group.NotifyFunc(func(storage.Invite) error { return nil }), quietLog())
	g, admin, err := groups.Create(ctx, "Наша", 1)
	if err != nil {
		t.Fatalf("группа: %v", err)
	}
	gs := store.ForGroup(g.ID)
	second, err := gs.AddMember(ctx, 2, storage.RoleMember)
	if err != nil {
		t.Fatalf("второй участник: %v", err)
	}

	otherGroup, otherMember, err := groups.Create(ctx, "Чужая", 9)
	if err != nil {
		t.Fatalf("чужая группа: %v", err)
	}

	// APP_DEV_USER_ID здесь не годится: он даёт одного человека на весь
	// сервер, а нам нужно ходить от разных. Подпись собираем настоящую.
	cfg := &config.Config{
		BotToken:  testToken,
		TZ:        time.UTC,
		AppListen: "127.0.0.1:0",
	}
	return &testServer{
		srv:     New(cfg, store, groups, quietLog()),
		store:   store,
		group:   gs,
		members: []storage.Member{admin, second},
		other:   store.ForGroup(otherGroup.ID),
		otherM:  otherMember,
	}
}

// do ходит в API от имени человека с настоящей подписью Telegram.
func (ts *testServer) do(t *testing.T, userID int64, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, reader)
	fields := validFields(time.Now())
	fields["user"] = `{"id":` + strconv.FormatInt(userID, 10) + `,"first_name":"Тест"}`
	r.Header.Set("Authorization", "tma "+signInitData(t, testToken, fields))

	w := httptest.NewRecorder()
	ts.srv.routes().ServeHTTP(w, r)
	return w
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("ответ не разобрать (%d): %s", w.Code, w.Body.String())
	}
	return out
}

func TestAPINeedsSignature(t *testing.T) {
	// Браузерной ветки нет: без подписи внутрь не попасть ничем.
	ts := newTestServer(t)

	for _, c := range []struct{ name, auth string }{
		{"без заголовка", ""},
		{"чужая схема", "Bearer secret"},
		{"мусор вместо подписи", "tma hash=deadbeef"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/state", nil)
			if c.auth != "" {
				r.Header.Set("Authorization", c.auth)
			}
			w := httptest.NewRecorder()
			ts.srv.routes().ServeHTTP(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("код = %d, ожидался 401", w.Code)
			}
		})
	}
}

func TestStateShowsOwnGroupOnly(t *testing.T) {
	ts := newTestServer(t)

	w := ts.do(t, 1, "GET", "/api/state", "")
	if w.Code != http.StatusOK {
		t.Fatalf("код = %d: %s", w.Code, w.Body.String())
	}
	state := decodeBody[stateResponse](t, w)

	if state.Group == nil || state.Group.Name != "Наша" {
		t.Fatalf("группа = %+v, ожидалась «Наша»", state.Group)
	}
	if state.Group.Role != storage.RoleAdmin || state.Group.MemberID != ts.members[0].ID {
		t.Errorf("про смотрящего = %+v, ожидался администратор", state.Group)
	}
	if len(state.Members) != 2 {
		t.Errorf("участников %d, ожидалось двое — чужие сюда попадать не должны", len(state.Members))
	}
	if len(state.Categories) != 14 {
		t.Errorf("категорий %d, ожидалось 14 из шаблона", len(state.Categories))
	}
	if state.MaxMembers != storage.MaxGroupSize {
		t.Errorf("потолок = %d, ожидался %d", state.MaxMembers, storage.MaxGroupSize)
	}
}

func TestStateWithoutGroupShowsOnlyInvites(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()

	if _, err := ts.group.Invite(ctx, 1, 50); err != nil {
		t.Fatalf("приглашение: %v", err)
	}

	state := decodeBody[stateResponse](t, ts.do(t, 50, "GET", "/api/state", ""))
	if state.Group != nil {
		t.Errorf("группа = %+v, у гостя её быть не должно", state.Group)
	}
	if len(state.Invites) != 1 || state.Invites[0].GroupName != "Наша" {
		t.Fatalf("приглашения = %+v, ожидалось одно в «Нашу»", state.Invites)
	}
	if state.Invites[0].Inviter != "Илья" {
		t.Errorf("зовущий = %q, ожидался Илья", state.Invites[0].Inviter)
	}
	// Категории чужой группы гость видеть не должен.
	if len(state.Categories) != 0 {
		t.Errorf("категорий %d, вне группы их быть не должно", len(state.Categories))
	}
}

func TestTransactionsAreScopedToGroup(t *testing.T) {
	// Главное, что вообще проверяет этот пакет: через HTTP не видно чужого.
	ts := newTestServer(t)
	ctx := context.Background()

	mine := mustInsert(t, ts.group, ts.members[0].ID, "1000", "наша трата")
	theirs := mustInsert(t, ts.other, ts.otherM.ID, "2000", "чужая трата")

	list := decodeBody[listResponse](t, ts.do(t, 1, "GET", "/api/transactions", ""))
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != mine {
		t.Fatalf("список = %+v, ожидалась только своя трата %d", list, mine)
	}

	// Чужую нельзя ни прочитать, ни изменить, ни удалить — даже зная её id.
	for _, c := range []struct {
		name, method, path, body string
	}{
		{"правка", "PATCH", "/api/transactions/" + itoa(theirs), `{"description":"моё"}`},
		{"удаление", "DELETE", "/api/transactions/" + itoa(theirs), ""},
		{"возврат", "POST", "/api/transactions/" + itoa(theirs) + "/restore", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := ts.do(t, 1, c.method, c.path, c.body)
			if w.Code != http.StatusNotFound {
				t.Errorf("код = %d, ожидался 404: %s", w.Code, w.Body.String())
			}
		})
	}

	// И чужая запись цела.
	tx, err := ts.other.Transaction(ctx, theirs)
	if err != nil || tx.Description != "чужая трата" {
		t.Errorf("чужая трата = %+v (%v), она не должна была измениться", tx, err)
	}
}

func TestUpdateChecksVersion(t *testing.T) {
	// Одну трату правят с двух телефонов чаще, чем кажется.
	ts := newTestServer(t)
	id := mustInsert(t, ts.group, ts.members[0].ID, "1000", "продукты")

	first := decodeBody[txJSON](t, ts.do(t, 1, "PATCH", "/api/transactions/"+itoa(id),
		`{"description":"пятёрочка"}`))
	if first.Description != "пятёрочка" {
		t.Fatalf("описание = %q, ожидалось «пятёрочка»", first.Description)
	}

	// Второй клиент помнит старую версию — его правку принимать нельзя.
	w := ts.do(t, 2, "PATCH", "/api/transactions/"+itoa(id),
		`{"description":"магнит","updated_at":"2020-01-01T00:00:00Z"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("код = %d, ожидался 409: %s", w.Code, w.Body.String())
	}

	// А с актуальной версией — можно: бюджет общий, правит любой участник.
	body := `{"description":"магнит","updated_at":"` + first.UpdatedAt + `"}`
	if w := ts.do(t, 2, "PATCH", "/api/transactions/"+itoa(id), body); w.Code != http.StatusOK {
		t.Errorf("код = %d, ожидался 200: %s", w.Code, w.Body.String())
	}
}

func TestUpdateRecipients(t *testing.T) {
	ts := newTestServer(t)
	id := mustInsert(t, ts.group, ts.members[0].ID, "1000", "продукты")

	body := `{"recipients":[` + itoa(ts.members[1].ID) + `]}`
	tx := decodeBody[txJSON](t, ts.do(t, 1, "PATCH", "/api/transactions/"+itoa(id), body))
	if len(tx.Recipients) != 1 || tx.Recipients[0] != ts.members[1].ID {
		t.Errorf("получатели = %v, ожидался участник %d", tx.Recipients, ts.members[1].ID)
	}

	// Пустой список — это «на всю группу», а не «не трогать».
	tx = decodeBody[txJSON](t, ts.do(t, 1, "PATCH", "/api/transactions/"+itoa(id), `{"recipients":[]}`))
	if len(tx.Recipients) != 0 {
		t.Errorf("получатели = %v, ожидался пустой список", tx.Recipients)
	}

	// Чужой участник не пройдёт: хранилище сверяет их с группой.
	body = `{"recipients":[` + itoa(ts.otherM.ID) + `]}`
	tx = decodeBody[txJSON](t, ts.do(t, 1, "PATCH", "/api/transactions/"+itoa(id), body))
	if len(tx.Recipients) != 0 {
		t.Errorf("получатели = %v, чужой участник попасть в них не должен", tx.Recipients)
	}
}

func TestGroupActionsRespectRoles(t *testing.T) {
	ts := newTestServer(t)

	// Обычный участник не зовёт и не исключает.
	if w := ts.do(t, 2, "POST", "/api/group/invite", `{"user_id":50}`); w.Code != http.StatusForbidden {
		t.Errorf("приглашение обычным участником: код = %d, ожидался 403", w.Code)
	}
	path := "/api/group/members/" + itoa(ts.members[0].ID)
	if w := ts.do(t, 2, "DELETE", path, ""); w.Code != http.StatusForbidden {
		t.Errorf("исключение обычным участником: код = %d, ожидался 403", w.Code)
	}

	// Администратор зовёт — и получает понятный отказ, если человек боту
	// ещё не писал.
	w := ts.do(t, 1, "POST", "/api/group/invite", `{"user_id":12345}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("код = %d, ожидался 404: %s", w.Code, w.Body.String())
	}
	if msg := decodeBody[map[string]string](t, w)["error"]; !strings.Contains(msg, "/start") {
		t.Errorf("текст = %q, он должен подсказывать, что делать", msg)
	}

	if w := ts.do(t, 1, "POST", "/api/group/invite", `{"user_id":50}`); w.Code != http.StatusOK {
		t.Errorf("приглашение администратором: код = %d, %s", w.Code, w.Body.String())
	}
}

func TestLastAdminCannotLeaveThroughAPI(t *testing.T) {
	ts := newTestServer(t)

	w := ts.do(t, 1, "POST", "/api/group/leave", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("код = %d, ожидался 409: %s", w.Code, w.Body.String())
	}
	if msg := decodeBody[map[string]string](t, w)["error"]; !strings.Contains(msg, "администратор") {
		t.Errorf("текст = %q, он должен объяснять причину", msg)
	}
}

func TestWithoutGroupMostThingsAreClosed(t *testing.T) {
	ts := newTestServer(t)

	for _, path := range []string{"/api/transactions", "/api/report/month", "/api/report/months"} {
		w := ts.do(t, 50, "GET", path, "")
		if w.Code != http.StatusPreconditionRequired {
			t.Errorf("%s: код = %d, ожидался 428", path, w.Code)
		}
	}
}

func TestMonthReportComesWithComparison(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()

	now := time.Now().UTC()
	// Трата в текущем месяце и трата в прошлом — чтобы было что сравнивать.
	if _, err := ts.group.InsertTransaction(ctx, storage.Transaction{
		PayerMemberID: ts.members[0].ID, Recipients: []int64{ts.members[0].ID},
		Kind: storage.KindExpense, Amount: decimal.RequireFromString("1000"),
		Description: "продукты", RawText: "продукты 1000",
		SpentAt: time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	m := decodeBody[monthResponse](t, ts.do(t, 1, "GET", "/api/report/month", ""))
	if m.Total != "1000" {
		t.Errorf("итог = %s, ожидалось 1000", m.Total)
	}
	if len(m.Categories) != 1 || m.Categories[0].Name != "Без категории" {
		t.Errorf("категории = %+v, ожидалась одна без категории", m.Categories)
	}
	if len(m.Payers) != 1 || m.Payers[0].ID != ts.members[0].ID {
		t.Errorf("плательщики = %+v, ожидался один", m.Payers)
	}
}

func TestMonthStripAlwaysHasThirteenColumns(t *testing.T) {
	// Пропуск в полосе читается как «данных нет», а не «трат не было».
	ts := newTestServer(t)

	months := decodeBody[[]map[string]any](t, ts.do(t, 1, "GET", "/api/report/months", ""))
	if len(months) != 13 {
		t.Fatalf("месяцев %d, ожидалось 13", len(months))
	}
	for _, m := range months {
		if m["amount"] == nil {
			t.Errorf("месяц без суммы: %+v", m)
		}
	}
}

func TestAvatarIsVisibleOnlyInsideTheGroup(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()

	if _, err := ts.store.SetAvatar(ctx, 2, []byte("не-настоящий-jpeg")); err != nil {
		t.Fatalf("фото: %v", err)
	}

	if w := ts.do(t, 1, "GET", "/api/avatar/2", ""); w.Code != http.StatusOK {
		t.Errorf("своей группе фото видно: код = %d", w.Code)
	}
	if w := ts.do(t, 9, "GET", "/api/avatar/2", ""); w.Code != http.StatusForbidden {
		t.Errorf("чужому фото видно быть не должно: код = %d", w.Code)
	}
}

func TestCategoryEditResetsGuessedWords(t *testing.T) {
	// Список категорий изменился — прежние догадки модели устарели.
	ts := newTestServer(t)
	ctx := context.Background()

	cats, _ := ts.group.Categories(ctx)
	if err := ts.group.UpsertWord(ctx, 1, "пиво", cats[0].ID, nil, storage.SourceLLM); err != nil {
		t.Fatalf("привязка моделью: %v", err)
	}
	if err := ts.group.UpsertWord(ctx, 1, "самокат", cats[0].ID, nil, storage.SourceManual); err != nil {
		t.Fatalf("ручная привязка: %v", err)
	}

	if w := ts.do(t, 1, "POST", "/api/categories", `{"name":"Алкоголь","hint":"пиво, вино","default_to":null}`); w.Code != http.StatusOK {
		t.Fatalf("создание категории: код = %d, %s", w.Code, w.Body.String())
	}

	hits, _ := ts.group.LookupWords(ctx, 1, []string{"пиво", "самокат"})
	if h, seen := hits["пиво"]; seen && h.Source == storage.SourceLLM {
		t.Error("догадка модели должна была уйти из словаря")
	}
	if hits["самокат"].Source != storage.SourceManual {
		t.Errorf("ручная привязка = %+v, её сброс не касается", hits["самокат"])
	}
}

func mustInsert(t *testing.T, g *storage.GroupStore, payer int64, amount, description string) int64 {
	t.Helper()
	id, err := g.InsertTransaction(context.Background(), storage.Transaction{
		PayerMemberID: payer, Recipients: []int64{payer},
		Kind: storage.KindExpense, Amount: decimal.RequireFromString(amount),
		Description: description, RawText: description, SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	return id
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
