package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testStore(t *testing.T) *storage.Store {
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

	// Очередь добора общая, и записи прошлых тестов ломали бы счётчик вызовов.
	if _, err := s.Pool().Exec(ctx, `truncate transactions restart identity`); err != nil {
		t.Fatalf("очистка: %v", err)
	}
	return s
}

// stubLLM возвращает заранее заданный разбор и считает обращения.
type stubLLM struct {
	calls int
	items []classify.RawItem
	err   error
}

func (s *stubLLM) Parse(context.Context, string, []storage.Category) ([]classify.RawItem, error) {
	s.calls++
	return s.items, s.err
}

func newBackfill(t *testing.T, llm classify.LLM, breaker *classify.Breaker, budget *classify.Budget) (*Backfill, *storage.Store) {
	t.Helper()
	store := testStore(t)
	svc := classify.NewService(store, llm, breaker, budget, quietLog())
	return New(store, svc, breaker, budget, quietLog()), store
}

func degradedTx(t *testing.T, store *storage.Store, userID int64, amount, raw string) int64 {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertUser(ctx, userID, "Тест"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	id, err := store.InsertTransaction(ctx, storage.Transaction{
		PayerID:             userID,
		Beneficiary:         classify.BenPayer,
		Kind:                classify.KindExpense,
		Amount:              decimal.RequireFromString(amount),
		Description:         raw,
		RawText:             raw,
		NeedsClassification: true,
		SpentAt:             time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	return id
}

func TestBackfillFillsCategory(t *testing.T) {
	llm := &stubLLM{items: []classify.RawItem{{
		Amount: "600", Description: "лимонад", Category: "Продукты",
		Beneficiary: classify.BenBoth, Kind: classify.KindExpense,
	}}}
	budget := classify.NewBudget(2_000_000, &zeroUsage{}, nil, quietLog())
	w, store := newBackfill(t, llm, classify.NewBreaker(time.Minute, quietLog()), budget)

	id := degradedTx(t, store, 501, "600", "600 лимонад")
	w.Tick(context.Background())

	tx, err := store.Transaction(context.Background(), id)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if tx.NeedsClassification {
		t.Error("после добора флаг должен быть снят")
	}
	if tx.CategoryName != "Продукты" {
		t.Errorf("категория = %q, ожидались Продукты", tx.CategoryName)
	}
	if tx.Beneficiary != classify.BenBoth {
		t.Errorf("бенефициар = %q, ожидался both из разбора", tx.Beneficiary)
	}
}

func TestBackfillRestoresLostAmounts(t *testing.T) {
	// «вчера пятёрочка 1200 и такси 400» при лежащем API сохранилось одной
	// записью на 1200 (§8). Вторая трата есть только в raw_text — воркер
	// обязан её дописать, иначе она потеряна навсегда.
	llm := &stubLLM{items: []classify.RawItem{
		{Amount: "1200", Description: "пятёрочка", Category: "Продукты",
			Beneficiary: classify.BenBoth, Kind: classify.KindExpense, DaysAgo: 1},
		{Amount: "400", Description: "такси", Category: "Такси",
			Beneficiary: classify.BenPayer, Kind: classify.KindExpense, DaysAgo: 1},
	}}
	budget := classify.NewBudget(2_000_000, &zeroUsage{}, nil, quietLog())
	w, store := newBackfill(t, llm, classify.NewBreaker(time.Minute, quietLog()), budget)

	ctx := context.Background()
	id := degradedTx(t, store, 505, "1200", "вчера пятёрочка 1200 и такси 400")
	w.Tick(ctx)

	from := time.Now().AddDate(0, 0, -3)
	to := time.Now().AddDate(0, 0, 1)
	rows, err := store.Expenses(ctx, from, to)
	if err != nil {
		t.Fatalf("расходы: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("записей %d (%+v), ожидались две — вторая трата не должна теряться (§8)", len(rows), rows)
	}

	// И дата обеих — вчерашняя, как сказала модель.
	tx, _ := store.Transaction(ctx, id)
	if got := time.Since(tx.SpentAt).Hours(); got < 20 || got > 28 {
		t.Errorf("дата траты сдвинута на %.0f часов, ожидались сутки (days_ago=1)", got)
	}
}

func TestBackfillFixesKindAndDoesNotMemorizeIncome(t *testing.T) {
	// Во время сбоя API доход записался расходом. Воркер обязан поправить
	// вид операции, иначе зарплата попадёт в «Всего» отчёта.
	llm := &stubLLM{items: []classify.RawItem{{
		Amount: "50000", Description: "зарплата", Category: "Прочее",
		Beneficiary: classify.BenPayer, Kind: classify.KindIncome,
	}}}
	budget := classify.NewBudget(2_000_000, &zeroUsage{}, nil, quietLog())
	w, store := newBackfill(t, llm, classify.NewBreaker(time.Minute, quietLog()), budget)

	ctx := context.Background()
	id := degradedTx(t, store, 506, "50000", "зарплата 50000")
	w.Tick(ctx)

	tx, _ := store.Transaction(ctx, id)
	if tx.Kind != classify.KindIncome {
		t.Errorf("вид операции = %q, ожидался income", tx.Kind)
	}
	hits, err := store.LookupWords(ctx, 506, []string{"зарплата"})
	if err != nil {
		t.Fatalf("словарь: %v", err)
	}
	if _, ok := hits["зарплата"]; ok {
		t.Error("слово дохода не должно уезжать в кэш: быстрый путь записал бы его расходом")
	}
}

func TestBackfillClosesStaleRecord(t *testing.T) {
	// Модель устойчиво не отвечает. Запись, провисевшую дольше staleAfter,
	// надо закрыть, иначе воркер будет ходить в сеть по ней вечно.
	llm := &stubLLM{err: &classify.Error{Kind: storage.ErrKindSchema, Err: context.Canceled}}
	budget := classify.NewBudget(2_000_000, &zeroUsage{}, nil, quietLog())
	w, store := newBackfill(t, llm, classify.NewBreaker(time.Minute, quietLog()), budget)

	ctx := context.Background()
	id := degradedTx(t, store, 507, "600", "600 непонятное")

	w.Tick(ctx)
	if tx, _ := store.Transaction(ctx, id); !tx.NeedsClassification {
		t.Fatal("свежую запись закрывать рано — модель могла отвечать не по схеме разово")
	}

	// Прошло больше двух часов.
	w.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	w.Tick(ctx)

	tx, _ := store.Transaction(ctx, id)
	if tx.NeedsClassification {
		t.Error("зависшая запись должна закрываться, а не запрашиваться вечно")
	}
	if llm.calls != 2 {
		t.Errorf("сетевых вызовов %d, ожидались два", llm.calls)
	}
}

func TestBackfillSkipsTickWhenBreakerOpen(t *testing.T) {
	llm := &stubLLM{}
	breaker := classify.NewBreaker(time.Hour, quietLog())
	for i := 0; i < 3; i++ {
		breaker.Record(&classify.Error{Kind: storage.ErrKindQuota, Err: context.DeadlineExceeded})
	}
	budget := classify.NewBudget(2_000_000, &zeroUsage{}, nil, quietLog())
	w, store := newBackfill(t, llm, breaker, budget)

	id := degradedTx(t, store, 502, "700", "700 что-то")
	w.Tick(context.Background())

	if llm.calls != 0 {
		t.Errorf("сетевых вызовов %d, при открытом breaker тик пропускается целиком (§12)", llm.calls)
	}
	tx, _ := store.Transaction(context.Background(), id)
	if !tx.NeedsClassification {
		t.Error("запись должна остаться в очереди")
	}
}

func TestBackfillSkipsTickWhenBudgetExhausted(t *testing.T) {
	llm := &stubLLM{}
	budget := classify.NewBudget(1000, &fixedUsage{total: 1000}, nil, quietLog())
	w, store := newBackfill(t, llm, classify.NewBreaker(time.Minute, quietLog()), budget)

	degradedTx(t, store, 503, "800", "800 что-то")
	w.Tick(context.Background())

	if llm.calls != 0 {
		t.Errorf("сетевых вызовов %d, при исчерпанном бюджете их быть не должно", llm.calls)
	}
}

func TestBackfillClosesUnparsableRecord(t *testing.T) {
	// Модель отвечает суммой, которой нет в тексте: разобрать нечего.
	// Запись обязана закрыться, иначе она будет возвращаться каждые десять
	// минут и жечь токены.
	llm := &stubLLM{items: []classify.RawItem{{
		Amount: "999999", Description: "ерунда", Category: "Продукты",
		Beneficiary: classify.BenBoth, Kind: classify.KindExpense,
	}}}
	budget := classify.NewBudget(2_000_000, &zeroUsage{}, nil, quietLog())
	w, store := newBackfill(t, llm, classify.NewBreaker(time.Minute, quietLog()), budget)

	id := degradedTx(t, store, 504, "900", "900 непонятное")
	w.Tick(context.Background())
	w.Tick(context.Background())

	tx, _ := store.Transaction(context.Background(), id)
	if tx.NeedsClassification {
		t.Error("неразобранная запись должна закрываться, а не висеть в очереди вечно")
	}
	if tx.CategoryName != classify.CategoryOther {
		t.Errorf("категория = %q, ожидалось «%s»", tx.CategoryName, classify.CategoryOther)
	}
	if llm.calls != 1 {
		t.Errorf("сетевых вызовов %d, ожидался один — второй тик уже не должен её брать", llm.calls)
	}
}

// zeroUsage — расход нулевой, бюджет всегда пропускает.
type zeroUsage struct{}

func (zeroUsage) MonthlyUsage(context.Context) (storage.MonthUsage, error) {
	return storage.MonthUsage{}, nil
}

type fixedUsage struct{ total int64 }

func (f *fixedUsage) MonthlyUsage(context.Context) (storage.MonthUsage, error) {
	return storage.MonthUsage{TotalTokens: f.total}, nil
}
