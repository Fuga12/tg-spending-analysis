package classify

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// fakeDict — словарь в памяти, чтобы проверять кэш без базы.
type fakeDict struct {
	words map[string]storage.WordHit
}

func (f *fakeDict) Categories(context.Context) ([]storage.Category, error) {
	return testCategories(), nil
}

func (f *fakeDict) LookupWords(_ context.Context, _ int64, words []string) (map[string]storage.WordHit, error) {
	out := map[string]storage.WordHit{}
	for _, w := range words {
		if h, ok := f.words[w]; ok {
			h.Word = w
			out[w] = h
		}
	}
	return out, nil
}

// countingLLM считает обращения к модели: их отсутствие — и есть проверка.
type countingLLM struct {
	calls int
	items []RawItem
}

func (c *countingLLM) Parse(context.Context, string, []storage.Category) ([]RawItem, error) {
	c.calls++
	return c.items, nil
}

// openGate — предохранители, которые всегда пропускают: их поведение
// проверяется отдельно, в breaker_test.go.
type openGate struct{}

func (openGate) Allow() bool  { return true }
func (openGate) Record(error) {}

type alwaysAllowBudget struct{}

func (alwaysAllowBudget) Allow(context.Context) bool { return true }

func newService(words map[string]storage.WordHit, llm *countingLLM) *Service {
	return NewService(&fakeDict{words: words}, llm, openGate{}, alwaysAllowBudget{}, quietLog())
}

func TestCacheHitSkipsAPI(t *testing.T) {
	llm := &countingLLM{}
	svc := newService(map[string]storage.WordHit{
		"пятёрочка": {CategoryID: 1, Source: storage.SourceLLM, Hits: 5},
	}, llm)

	res, err := svc.Classify(context.Background(), 1, "пятёрочка 1200")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 0 {
		t.Errorf("модель вызвана %d раз, ожидалось 0", llm.calls)
	}
	if res.Source != SourceCache {
		t.Errorf("источник = %q, ожидался cache", res.Source)
	}
	if len(res.Items) != 1 || !res.Items[0].Amount.Equal(dec("1200")) {
		t.Fatalf("ожидалась одна трата на 1200, получено %+v", res.Items)
	}
	// beneficiary у слова не задан — берётся default_beneficiary категории.
	if res.Items[0].Beneficiary != BenBoth {
		t.Errorf("beneficiary = %q, ожидался both из категории", res.Items[0].Beneficiary)
	}
}

func TestSeedOnlyWordSkipsAPI(t *testing.T) {
	llm := &countingLLM{}
	svc := newService(map[string]storage.WordHit{
		"такси": {CategoryID: 2, Source: storage.SourceSeed},
	}, llm)

	res, err := svc.Classify(context.Background(), 1, "такси 450")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 0 {
		t.Errorf("модель вызвана %d раз, ожидалось 0", llm.calls)
	}
	if res.Items[0].Beneficiary != BenPayer {
		t.Errorf("beneficiary = %q, ожидался payer из категории Такси", res.Items[0].Beneficiary)
	}
}

func TestComplexityTriggerForcesAPI(t *testing.T) {
	llm := &countingLLM{items: []RawItem{
		raw("1200", "пятёрочка", "Продукты", BenBoth, KindExpense, 0),
		raw("400", "такси", "Такси", BenPayer, KindExpense, 0),
	}}
	svc := newService(map[string]storage.WordHit{
		"пятёрочка": {CategoryID: 1, Source: storage.SourceLLM, Hits: 5},
		"такси":     {CategoryID: 2, Source: storage.SourceSeed},
	}, llm)

	// Слова в кэше есть, но «и» означает перечисление — словарь не справится.
	res, err := svc.Classify(context.Background(), 1, "пятёрочка 1200 и такси 400")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("модель вызвана %d раз, ожидался 1", llm.calls)
	}
	if res.Source != SourceLLM || len(res.Items) != 2 {
		t.Errorf("ожидались два элемента от модели, получено %d (%s)", len(res.Items), res.Source)
	}
}

func TestUnknownWordGoesToAPI(t *testing.T) {
	llm := &countingLLM{items: []RawItem{raw("600", "лимонад", "Продукты", BenBoth, KindExpense, 0)}}
	svc := newService(map[string]storage.WordHit{}, llm)

	if _, err := svc.Classify(context.Background(), 1, "600 лимонад"); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("модель вызвана %d раз, ожидался 1", llm.calls)
	}
}

func TestTwoAmountsGoToAPI(t *testing.T) {
	llm := &countingLLM{items: []RawItem{raw("4000", "такси", "Такси", BenPayer, KindExpense, 0)}}
	svc := newService(map[string]storage.WordHit{
		"такси": {CategoryID: 2, Source: storage.SourceSeed},
		"обед":  {CategoryID: 1, Source: storage.SourceSeed},
	}, llm)

	// Оба слова в словаре, но сумм две — словарь не берётся (§5, условие 1).
	if _, err := svc.Classify(context.Background(), 1, "такси 4000 обед 350"); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("модель вызвана %d раз, ожидался 1", llm.calls)
	}
}

func TestManualBeatsLLMAndSeed(t *testing.T) {
	llm := &countingLLM{}
	svc := newService(map[string]storage.WordHit{
		// Пользователь руками сказал, что «самокат» — это Такси,
		// хотя затравка считает его Продуктами, а модель — тоже Продуктами.
		"самокат":  {CategoryID: 2, Source: storage.SourceManual, Beneficiary: BenPayer, Hits: 1},
		"вечерний": {CategoryID: 1, Source: storage.SourceLLM, Hits: 99},
	}, llm)

	res, err := svc.Classify(context.Background(), 1, "вечерний самокат 250")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 0 {
		t.Errorf("модель вызвана %d раз, ожидалось 0", llm.calls)
	}
	if res.Items[0].CategoryID == nil || *res.Items[0].CategoryID != 2 {
		t.Errorf("категория = %v, ожидалась ручная (2)", res.Items[0].CategoryID)
	}
	if res.Items[0].Beneficiary != BenPayer {
		t.Errorf("beneficiary = %q, ожидался payer из ручной правки", res.Items[0].Beneficiary)
	}
}

func TestHitsBreakTieInsideWordMap(t *testing.T) {
	llm := &countingLLM{}
	svc := newService(map[string]storage.WordHit{
		"кофе":   {CategoryID: 1, Source: storage.SourceLLM, Hits: 2},
		"утренн": {CategoryID: 2, Source: storage.SourceLLM, Hits: 40},
	}, llm)

	res, err := svc.Classify(context.Background(), 1, "утренн кофе 250")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Items[0].CategoryID == nil || *res.Items[0].CategoryID != 2 {
		t.Errorf("категория = %v, ожидалась с максимальным hits (2)", res.Items[0].CategoryID)
	}
}

func TestNoAmountIsAnError(t *testing.T) {
	llm := &countingLLM{}
	svc := newService(map[string]storage.WordHit{}, llm)

	_, err := svc.Classify(context.Background(), 1, "кофе")
	if err != ErrNoAmount {
		t.Fatalf("ошибка = %v, ожидалась ErrNoAmount", err)
	}
	if llm.calls != 0 {
		t.Errorf("модель вызвана %d раз, ожидалось 0", llm.calls)
	}
}

func TestSignificantWords(t *testing.T) {
	got := SignificantWords("взял в пятёрочке на 1200 для дома")
	want := map[string]bool{"взял": true, "пятёрочке": true, "дома": true}

	if len(got) != len(want) {
		t.Fatalf("значимые слова = %v, ожидалось %d слов", got, len(want))
	}
	for _, w := range got {
		if !want[w] {
			t.Errorf("лишнее слово %q в %v", w, got)
		}
	}
}

func TestCacheHitWithCurrencyTail(t *testing.T) {
	// «кофе 500р»: валютный хвост не должен ломать быстрый путь.
	llm := &countingLLM{}
	svc := newService(map[string]storage.WordHit{
		"кофе": {CategoryID: 1, Source: storage.SourceSeed},
	}, llm)

	res, err := svc.Classify(context.Background(), 1, "кофе 500р")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if llm.calls != 0 {
		t.Errorf("модель вызвана %d раз, ожидалось 0", llm.calls)
	}
	if res.Items[0].Description != "кофе" {
		t.Errorf("описание = %q, ожидалось «кофе»", res.Items[0].Description)
	}
}

func TestCacheDoesNotRelabelForeignWords(t *testing.T) {
	// Победила ручная привязка «самокат» → Такси. Слово «вечерний» указывало
	// на другую категорию, и переучивать его на Такси нельзя.
	llm := &countingLLM{}
	svc := newService(map[string]storage.WordHit{
		"самокат":  {CategoryID: 2, Source: storage.SourceManual, Hits: 1},
		"вечерний": {CategoryID: 1, Source: storage.SourceLLM, Hits: 99},
	}, llm)

	res, err := svc.Classify(context.Background(), 1, "вечерний самокат 250")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(res.Items[0].Words) != 1 || res.Items[0].Words[0] != "самокат" {
		t.Errorf("в кэш уедут слова %v, ожидалось только «самокат»", res.Items[0].Words)
	}
}

func TestDescribeDropsTrailingStopWord(t *testing.T) {
	// «пиво на 4000» без обрезки давало описание «пиво на» — с ним трата
	// и уезжала в отчёты.
	cases := map[string]string{
		"пиво на 4000":     "пиво",
		"за такси 500":     "такси",
		"на продукты 1200": "продукты",
		"кофе 300":         "кофе",
	}
	for in, want := range cases {
		if got := Describe(in); got != want {
			t.Errorf("Describe(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}
