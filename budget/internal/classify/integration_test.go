package classify

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"budget/internal/storage"
)

// testStore — то же хранилище, что у бота, на настоящей базе.
func testStore(t *testing.T) *storage.Store {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан — интеграционные тесты пропущены")
	}
	ctx := context.Background()
	if err := storage.Migrate(ctx, dsn); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	s, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// clearWord убирает слово из личного кэша напрямую: хранилище такого метода
// не даёт и не должно, это нужно только тесту.
func clearWord(t *testing.T, userID int64, word string) {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("подключение для очистки: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx,
		`delete from word_map where user_id = $1 and word = $2`, userID, word); err != nil {
		t.Fatalf("очистка: %v", err)
	}
}

// TestSecondTimeResolvesFromCache — проверка фазы 4 целиком: первое сообщение
// уходит в модель, слово запоминается, второе такое же обходится без сети.
func TestSecondTimeResolvesFromCache(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	const userID = int64(777)
	if err := store.UpsertUser(ctx, userID, "Тест"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	// Слово заведомо не из затравки, но могло остаться от прошлого прогона.
	clearWord(t, userID, "лимонадница")

	llm := &countingLLM{items: []RawItem{
		raw("600", "лимонадница", "Продукты", BenBoth, KindExpense, 0),
	}}
	svc := NewService(store, llm, openGate{}, denyBudget{}, quietLog())

	first, err := svc.Classify(ctx, userID, "600 лимонадница")
	if err != nil {
		t.Fatalf("первый разбор: %v", err)
	}
	if first.Source != SourceLLM || llm.calls != 1 {
		t.Fatalf("первый разбор должен идти в модель: source=%s calls=%d", first.Source, llm.calls)
	}

	// Так же, как это делает обработчик бота после успешной записи (§8).
	for _, w := range first.Items[0].Words {
		if err := store.UpsertWord(ctx, userID, w, *first.Items[0].CategoryID,
			first.Items[0].Beneficiary, storage.SourceLLM); err != nil {
			t.Fatalf("запись в кэш: %v", err)
		}
	}

	second, err := svc.Classify(ctx, userID, "лимонадница 750")
	if err != nil {
		t.Fatalf("второй разбор: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("сетевых вызовов %d, второй раз слово должно резолвиться из кэша", llm.calls)
	}
	if second.Source != SourceCache {
		t.Errorf("источник = %q, ожидался cache", second.Source)
	}
	if second.Items[0].CategoryID == nil || *second.Items[0].CategoryID != *first.Items[0].CategoryID {
		t.Errorf("категория из кэша = %v, ожидалась та же, что у модели", second.Items[0].CategoryID)
	}
	if !second.Items[0].Amount.Equal(dec("750")) {
		t.Errorf("сумма = %s, ожидалось 750", second.Items[0].Amount)
	}
}
