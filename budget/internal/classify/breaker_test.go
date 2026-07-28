package classify

import (
	"context"
	"errors"
	"testing"
	"time"

	"budget/internal/storage"
)

func quotaErr() error { return &Error{Kind: storage.ErrKindQuota, Err: errors.New("429")} }
func httpErr() error  { return &Error{Kind: storage.ErrKindHTTP, Err: errors.New("502")} }

// clock — управляемое время, чтобы не спать в тестах.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestBreaker() (*Breaker, *clock) {
	c := &clock{t: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	b := NewBreaker(30*time.Minute, quietLog())
	b.now = c.now
	return b, c
}

func TestBreakerOpensAfterThreeErrors(t *testing.T) {
	b, _ := newTestBreaker()

	for i := 0; i < 2; i++ {
		b.Record(quotaErr())
		if !b.Allow() {
			t.Fatalf("после %d ошибок breaker не должен быть открыт", i+1)
		}
	}
	b.Record(quotaErr())

	if b.Allow() {
		t.Error("после трёх подряд ошибок quota сетевых вызовов быть не должно (§7)")
	}
	if open, until := b.State(); !open || until.IsZero() {
		t.Errorf("состояние = открыт %v до %v, ожидался открытый breaker", open, until)
	}
}

func TestBreakerCountsOnlyServiceErrors(t *testing.T) {
	b, _ := newTestBreaker()

	// Ответ не по схеме — это не «API недоступен», в счётчик он не идёт (§7).
	for i := 0; i < 5; i++ {
		b.Record(&Error{Kind: storage.ErrKindSchema, Err: errors.New("не по схеме")})
	}
	if !b.Allow() {
		t.Error("schema-ошибки не должны открывать breaker")
	}

	b.Record(httpErr())
	b.Record(httpErr())
	b.Record(nil) // успех обнуляет счётчик
	b.Record(httpErr())
	b.Record(httpErr())
	if !b.Allow() {
		t.Error("успешный вызов должен был обнулить счётчик подряд идущих ошибок")
	}
}

func TestBreakerProbeAfterCooldown(t *testing.T) {
	b, c := newTestBreaker()
	for i := 0; i < 3; i++ {
		b.Record(quotaErr())
	}

	c.add(29 * time.Minute)
	if b.Allow() {
		t.Fatal("время остывания ещё не вышло")
	}

	c.add(2 * time.Minute)
	if !b.Allow() {
		t.Fatal("после остывания должна выпускаться одна пробная попытка")
	}
	if b.Allow() {
		t.Error("пробная попытка должна быть ровно одна")
	}

	// Проба провалилась — снова закрываемся на полный срок.
	b.Record(quotaErr())
	if b.Allow() {
		t.Error("после неудачной пробы breaker должен закрыться снова")
	}

	c.add(31 * time.Minute)
	if !b.Allow() {
		t.Fatal("ожидалась новая проба")
	}
	b.Record(nil)
	if !b.Allow() || func() bool { open, _ := b.State(); return open }() {
		t.Error("успешная проба должна закрывать breaker")
	}
}

// countingBreaker — предохранитель, который всегда запрещает сеть.
type closedBreaker struct{ recorded int }

func (c *closedBreaker) Allow() bool      { return false }
func (c *closedBreaker) Record(err error) { c.recorded++ }

type denyBudget struct{}

func (denyBudget) Allow(context.Context) bool { return true }

type exhaustedBudget struct{}

func (exhaustedBudget) Allow(context.Context) bool { return false }

func TestOpenBreakerSkipsNetworkAndDegrades(t *testing.T) {
	llm := &countingLLM{}
	svc := NewService(&fakeDict{}, llm, &closedBreaker{}, denyBudget{}, quietLog())

	res, err := svc.Classify(context.Background(), 1, "600 лимонад")
	if err != nil {
		t.Fatalf("запись не должна теряться ни при каких условиях (§8): %v", err)
	}
	if llm.calls != 0 {
		t.Errorf("при открытом breaker сетевых вызовов быть не должно, было %d", llm.calls)
	}
	assertDegraded(t, res, "600")
}

func TestExhaustedBudgetSkipsNetworkAndDegrades(t *testing.T) {
	llm := &countingLLM{}
	svc := NewService(&fakeDict{}, llm, openGate{}, exhaustedBudget{}, quietLog())

	res, err := svc.Classify(context.Background(), 1, "лимонад 600")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 0 {
		t.Errorf("при исчерпанном бюджете сетевых вызовов быть не должно, было %d", llm.calls)
	}
	assertDegraded(t, res, "600")
}

// failingLLM всегда возвращает ошибку сервиса.
type failingLLM struct{ calls int }

func (f *failingLLM) Parse(context.Context, string, []storage.Category) ([]RawItem, error) {
	f.calls++
	return nil, httpErr()
}

func TestAPIFailureDegradesInsteadOfLosingRecord(t *testing.T) {
	llm := &failingLLM{}
	svc := NewService(&fakeDict{}, llm, openGate{}, denyBudget{}, quietLog())

	res, err := svc.Classify(context.Background(), 1, "вчера пятёрочка 1200")
	if err != nil {
		t.Fatalf("потеря записи из-за отказа API недопустима (§8): %v", err)
	}
	assertDegraded(t, res, "1200")
	if res.Items[0].Description != "вчера пятёрочка" {
		t.Errorf("описание = %q, ожидался текст без суммы", res.Items[0].Description)
	}
}

func TestEmptyItemsAfterValidationDegrade(t *testing.T) {
	// Модель вернула сумму, которой нет в тексте, — всё отфильтровалось.
	llm := &countingLLM{items: []RawItem{raw("99999", "лимонад", "Продукты", BenBoth, KindExpense, 0)}}
	svc := NewService(&fakeDict{}, llm, openGate{}, denyBudget{}, quietLog())

	res, err := svc.Classify(context.Background(), 1, "600 лимонад")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	assertDegraded(t, res, "600")
}

func TestBreakerLearnsFromServiceCalls(t *testing.T) {
	// Три сообщения подряд с ошибкой сервиса — и бот перестаёт ходить в сеть.
	b, _ := newTestBreaker()
	llm := &failingLLM{}
	svc := NewService(&fakeDict{}, llm, b, denyBudget{}, quietLog())

	for i := 0; i < 4; i++ {
		if _, err := svc.Classify(context.Background(), 1, "600 лимонад"); err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
	}
	if llm.calls != 3 {
		t.Errorf("сетевых вызовов %d, ожидались три до открытия breaker", llm.calls)
	}
}

func assertDegraded(t *testing.T, res *Result, amount string) {
	t.Helper()
	if res.Source != SourceDegraded {
		t.Errorf("источник = %q, ожидался degraded", res.Source)
	}
	if len(res.Items) != 1 {
		t.Fatalf("элементов %d, ожидался один", len(res.Items))
	}
	it := res.Items[0]
	if !it.Amount.Equal(dec(amount)) {
		t.Errorf("сумма = %s, ожидалась %s", it.Amount, amount)
	}
	if !it.NeedsClassification {
		t.Error("деградированная запись должна быть помечена needs_classification")
	}
	if it.CategoryID != nil {
		t.Errorf("категория = %v, у деградированной записи её быть не должно", *it.CategoryID)
	}
	if it.Beneficiary != BenPayer || it.Kind != KindExpense {
		t.Errorf("beneficiary/kind = %s/%s, ожидались payer/expense", it.Beneficiary, it.Kind)
	}
	if it.DaysAgo != 0 {
		t.Errorf("days_ago = %d, ожидался 0", it.DaysAgo)
	}
}

func TestBreakerNeverSticksOpen(t *testing.T) {
	// Пробная попытка провалилась ошибкой, которая в счётчик не идёт.
	// Breaker обязан вернуться к обычному циклу остывания, а не залипнуть.
	b, c := newTestBreaker()
	for i := 0; i < 3; i++ {
		b.Record(quotaErr())
	}

	c.add(31 * time.Minute)
	if !b.Allow() {
		t.Fatal("ожидалась пробная попытка")
	}
	b.Record(&Error{Kind: storage.ErrKindTimeout, Err: errors.New("не дождались")})

	if open, _ := b.State(); !open {
		t.Error("после неудачной пробы breaker должен считаться открытым")
	}
	c.add(31 * time.Minute)
	if !b.Allow() {
		t.Fatal("breaker залип открытым: новой пробы не будет никогда")
	}
	b.Record(nil)
	if !b.Allow() {
		t.Error("успешная проба должна закрывать breaker")
	}
}

func TestBudgetCheckedBeforeBreakerProbe(t *testing.T) {
	// Исчерпанный бюджет не должен съедать пробную попытку breaker: иначе
	// с наступлением нового месяца сеть так и не откроется (§7).
	b, c := newTestBreaker()
	for i := 0; i < 3; i++ {
		b.Record(quotaErr())
	}
	c.add(31 * time.Minute)

	llm := &countingLLM{items: []RawItem{raw("600", "лимонад", "Продукты", BenBoth, KindExpense, 0)}}
	svc := NewService(&fakeDict{}, llm, b, exhaustedBudget{}, quietLog())
	if _, err := svc.Classify(context.Background(), 1, "600 лимонад"); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	// Новый месяц: бюджет отпустил — проба должна быть на месте.
	svc = NewService(&fakeDict{}, llm, b, denyBudget{}, quietLog())
	res, err := svc.Classify(context.Background(), 1, "600 лимонад")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("сетевых вызовов %d, ожидался один — проба не должна была сгореть", llm.calls)
	}
	if res.Source != SourceLLM {
		t.Errorf("источник = %q, ожидался llm", res.Source)
	}
}

func TestZeroAmountIsNotAnExpense(t *testing.T) {
	// В базе стоит check (amount > 0): деградировать ноль нельзя, иначе
	// вставка упадёт и запись потеряется.
	llm := &failingLLM{}
	svc := NewService(&fakeDict{}, llm, openGate{}, denyBudget{}, quietLog())

	if _, err := svc.Classify(context.Background(), 1, "0 тест"); err != ErrNoAmount {
		t.Errorf("ошибка = %v, ожидалась ErrNoAmount", err)
	}
}

func TestSchemaErrorResetsFailureStreak(t *testing.T) {
	// Ответ не по схеме означает, что сервис жив: серия отказов прерывается.
	b, _ := newTestBreaker()
	b.Record(httpErr())
	b.Record(httpErr())
	b.Record(&Error{Kind: storage.ErrKindSchema, Err: errors.New("не по схеме")})
	b.Record(httpErr())

	if !b.Allow() {
		t.Error("breaker открылся по ошибкам, идущим не подряд")
	}
}
