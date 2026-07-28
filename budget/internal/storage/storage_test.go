package storage

import (
	"context"
	"os"
	"testing"
	"time"
)

// testStore поднимает хранилище на настоящей базе. Без TEST_DATABASE_URL
// тесты пропускаются: SQL нельзя проверить фейком, а гонять его вслепую —
// значит не проверять вовсе.
func testStore(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан — интеграционные тесты пропущены")
	}

	ctx := context.Background()
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	s, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(s.Close)

	// Затравка и категории остаются: они часть схемы.
	if _, err := s.pool.Exec(ctx, `truncate transactions, word_map, llm_usage, users restart identity cascade`); err != nil {
		t.Fatalf("очистка: %v", err)
	}
	return s
}

func TestMigrationsSeedSchema(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	cats, err := s.Categories(ctx)
	if err != nil {
		t.Fatalf("категории: %v", err)
	}
	if len(cats) != 14 {
		t.Errorf("категорий %d, в плане ровно 14", len(cats))
	}
	if cats[0].Name != "Продукты" || cats[len(cats)-1].Name != "Прочее" {
		t.Errorf("порядок категорий = %s ... %s", cats[0].Name, cats[len(cats)-1].Name)
	}

	var seeded int
	if err := s.pool.QueryRow(ctx, `select count(*) from word_seed`).Scan(&seeded); err != nil {
		t.Fatalf("затравка: %v", err)
	}
	if seeded != 82 {
		t.Errorf("слов в затравке %d, ожидалось 82", seeded)
	}

	// Неоднозначные слова сеять нельзя (§4).
	var ambiguous int
	err = s.pool.QueryRow(ctx,
		`select count(*) from word_seed where word = any($1)`,
		[]string{"метро", "озон", "вб", "вайлдберриз"}).Scan(&ambiguous)
	if err != nil {
		t.Fatalf("проверка неоднозначных: %v", err)
	}
	if ambiguous != 0 {
		t.Errorf("в затравке %d неоднозначных слов, их там быть не должно", ambiguous)
	}
}

func TestLookupWordsPrefersPersonalCache(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.UpsertUser(ctx, 1, "Илья"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	cats, _ := s.Categories(ctx)
	taxi := categoryID(t, cats, "Такси")

	// «самокат» в затравке — Продукты. Пользователь сказал: это Такси.
	if err := s.UpsertWord(ctx, 1, "самокат", taxi, "payer", SourceManual); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hits, err := s.LookupWords(ctx, 1, []string{"самокат", "такси", "неизвестноеслово"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("найдено %d слов, ожидалось два", len(hits))
	}
	if h := hits["самокат"]; h.Source != SourceManual || h.CategoryID != taxi {
		t.Errorf("«самокат» = %+v, ожидалась ручная привязка к Такси", h)
	}
	if h := hits["такси"]; h.Source != SourceSeed {
		t.Errorf("«такси» = %+v, ожидалась затравка", h)
	}
	if _, ok := hits["неизвестноеслово"]; ok {
		t.Error("незнакомое слово не должно находиться")
	}
}

func TestUpsertWordDoesNotOverwriteManual(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.UpsertUser(ctx, 1, "Илья"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	cats, _ := s.Categories(ctx)
	taxi := categoryID(t, cats, "Такси")
	food := categoryID(t, cats, "Продукты")

	if err := s.UpsertWord(ctx, 1, "самокат", taxi, "payer", SourceManual); err != nil {
		t.Fatalf("ручная привязка: %v", err)
	}
	// Модель считает иначе — и не должна перебить пользователя (§8).
	if err := s.UpsertWord(ctx, 1, "самокат", food, "both", SourceLLM); err != nil {
		t.Fatalf("привязка моделью: %v", err)
	}

	hits, _ := s.LookupWords(ctx, 1, []string{"самокат"})
	h := hits["самокат"]
	if h.CategoryID != taxi || h.Source != SourceManual || h.Beneficiary != "payer" {
		t.Errorf("после ответа модели = %+v, ожидалась сохранённая ручная привязка", h)
	}
	if h.Hits != 2 {
		t.Errorf("hits = %d, ожидалось 2 — счётчик растёт в любом случае", h.Hits)
	}

	// А вот новая ручная правка перебивает старую.
	if err := s.UpsertWord(ctx, 1, "самокат", food, "both", SourceManual); err != nil {
		t.Fatalf("вторая ручная правка: %v", err)
	}
	hits, _ = s.LookupWords(ctx, 1, []string{"самокат"})
	if hits["самокат"].CategoryID != food {
		t.Errorf("категория = %d, ожидалась новая ручная (%d)", hits["самокат"].CategoryID, food)
	}
}

func TestMonthlyUsageIgnoresPreviousMonth(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.RecordUsage(ctx, "gpt://f/m", 650, 50, true, ""); err != nil {
		t.Fatalf("запись расхода: %v", err)
	}
	if err := s.RecordUsage(ctx, "gpt://f/m", 0, 0, false, ErrKindQuota); err != nil {
		t.Fatalf("запись расхода: %v", err)
	}
	// Строка из прошлого месяца в текущий счётчик попадать не должна (§11).
	_, err := s.pool.Exec(ctx, `
		insert into llm_usage (created_at, model, prompt_tokens, completion_tokens, ok)
		values (date_trunc('month', now()) - interval '3 days', 'gpt://f/m', 1000000, 1000, true)`)
	if err != nil {
		t.Fatalf("прошлый месяц: %v", err)
	}

	used, err := s.MonthlyUsage(ctx)
	if err != nil {
		t.Fatalf("расход: %v", err)
	}
	if used.TotalTokens != 700 {
		t.Errorf("токенов за месяц %d, ожидалось 700 — прошлый месяц не считается", used.TotalTokens)
	}
	if used.Calls != 2 || used.Failed != 1 {
		t.Errorf("вызовов %d, неуспешных %d, ожидалось 2 и 1", used.Calls, used.Failed)
	}
}

func TestRecordUsageKeepsErrorKind(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, kind := range []string{ErrKindTimeout, ErrKindQuota, ErrKindHTTP, ErrKindSchema, ErrKindOther} {
		if err := s.RecordUsage(ctx, "gpt://f/m", 0, 0, false, kind); err != nil {
			t.Fatalf("вид ошибки %q не записался: %v", kind, err)
		}
	}
	var stored int
	if err := s.pool.QueryRow(ctx,
		`select count(distinct error_kind) from llm_usage where created_at > $1`,
		time.Now().Add(-time.Minute)).Scan(&stored); err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if stored != 5 {
		t.Errorf("различных видов ошибок %d, ожидалось 5", stored)
	}
}

func categoryID(t *testing.T, cats []Category, name string) int32 {
	t.Helper()
	for _, c := range cats {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("категория %q не найдена", name)
	return 0
}
