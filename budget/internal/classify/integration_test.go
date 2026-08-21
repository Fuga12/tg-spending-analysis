package classify

import (
	"context"
	"os"
	"testing"

	"budget/internal/storage"
)

// testGroup поднимает настоящее хранилище и заводит группу из одного человека.
// Проверять словарь фейком бессмысленно: весь смысл в том, что слово доезжает
// до базы и возвращается оттуда.
func testGroup(t *testing.T) (*storage.Store, *storage.GroupStore, storage.Member) {
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
	s, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(s.Close)

	if _, err := s.Pool().Exec(ctx, `
		truncate transactions, tx_recipients, word_map, llm_usage,
		         categories, invites, members, groups, users
		restart identity cascade`); err != nil {
		t.Fatalf("очистка: %v", err)
	}

	const userID = int64(777)
	if err := s.EnsureUser(ctx, userID, "Тест"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	g, member, err := s.CreateGroup(ctx, "Тест", userID)
	if err != nil {
		t.Fatalf("группа: %v", err)
	}
	return s, s.ForGroup(g.ID), member
}

// TestSecondTimeResolvesFromCache — проверка быстрого пути целиком: первое
// сообщение уходит в модель, слово запоминается, второе такое же обходится
// без сети.
func TestSecondTimeResolvesFromCache(t *testing.T) {
	_, group, member := testGroup(t)
	ctx := context.Background()

	// Слово заведомо не из затравки.
	llm := &countingLLM{items: []RawItem{
		raw("600", "лимонадница", "Продукты", KindExpense, 0),
	}}
	svc := NewService(llm, openGate{}, denyBudget{}, quietLog())
	sc := Scope{UserID: member.UserID, Dict: group, Usage: group}

	first, err := svc.Classify(ctx, sc, "600 лимонадница")
	if err != nil {
		t.Fatalf("первый разбор: %v", err)
	}
	if first.Source != SourceLLM || llm.calls != 1 {
		t.Fatalf("первый разбор должен идти в модель: source=%s calls=%d", first.Source, llm.calls)
	}

	// Так же, как это делает обработчик бота после успешной записи (§8).
	for _, w := range first.Items[0].Words {
		if err := group.UpsertWord(ctx, member.UserID, w, *first.Items[0].CategoryID,
			nil, storage.SourceLLM); err != nil {
			t.Fatalf("запись в словарь: %v", err)
		}
	}

	second, err := svc.Classify(ctx, sc, "лимонадница 750")
	if err != nil {
		t.Fatalf("второй разбор: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("сетевых вызовов %d, второй раз слово должно резолвиться из словаря", llm.calls)
	}
	if second.Source != SourceCache {
		t.Errorf("источник = %q, ожидался cache", second.Source)
	}
	if second.Items[0].CategoryID == nil || *second.Items[0].CategoryID != *first.Items[0].CategoryID {
		t.Errorf("категория из словаря = %v, ожидалась та же, что у модели", second.Items[0].CategoryID)
	}
	if !second.Items[0].Amount.Equal(dec("750")) {
		t.Errorf("сумма = %s, ожидалось 750", second.Items[0].Amount)
	}
}
