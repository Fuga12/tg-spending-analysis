package classify

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"budget/internal/storage"
)

// testPayer — кто платит в тестах разбора: умолчания категорий считаются
// относительно него.
const testPayer int64 = 1

func testCategories() []storage.Category {
	return []storage.Category{
		{ID: 1, Name: "Продукты", DefaultBeneficiary: BenBoth, SortOrder: 10},
		{ID: 2, Name: "Такси", DefaultBeneficiary: BenPayer, SortOrder: 50},
		{ID: 3, Name: "Прочее", DefaultBeneficiary: BenPayer, SortOrder: 140},
	}
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func raw(amount, description, category, beneficiary, kind string, daysAgo int) RawItem {
	return RawItem{
		Amount:      json.Number(amount),
		Description: description,
		Category:    category,
		Beneficiary: beneficiary,
		Kind:        kind,
		DaysAgo:     daysAgo,
	}
}

func TestValidateAmountMustComeFromText(t *testing.T) {
	// Модель «пересчитала» 1 200 в 12000 — такому элементу верить нельзя.
	items := Validate(
		[]RawItem{raw("12000", "продукты", "Продукты", BenBoth, KindExpense, 0)},
		"1 200 продукты", testCategories(), testPayer, quietLog())

	if len(items) != 0 {
		t.Fatalf("элемент с чужой суммой должен быть отброшен, получено %d", len(items))
	}
}

func TestValidateKeepsAmountFromText(t *testing.T) {
	items := Validate(
		[]RawItem{raw("1200", "продукты", "Продукты", BenBoth, KindExpense, 0)},
		"1 200 продукты", testCategories(), testPayer, quietLog())

	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	if !items[0].Amount.Equal(dec("1200")) {
		t.Errorf("сумма = %s, ожидалось 1200", items[0].Amount)
	}
	if items[0].CategoryID == nil || *items[0].CategoryID != 1 {
		t.Errorf("категория = %v, ожидались Продукты (1)", items[0].CategoryID)
	}
}

func TestValidateUnknownCategoryFallsBackToOther(t *testing.T) {
	items := Validate(
		[]RawItem{raw("600", "лимонад", "Криптовалюта", BenBoth, KindExpense, 0)},
		"600 лимонад", testCategories(), testPayer, quietLog())

	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	if items[0].CategoryID == nil || *items[0].CategoryID != 3 {
		t.Errorf("категория = %v, ожидалось Прочее (3)", items[0].CategoryID)
	}
}

func TestValidateTransferForcesPartnerAndDropsCategory(t *testing.T) {
	items := Validate(
		[]RawItem{raw("5000", "перевод", "Прочее", BenBoth, KindTransfer, 0)},
		"скинул ей 5к", testCategories(), testPayer, quietLog())

	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	if items[0].Beneficiary != BenPartner {
		t.Errorf("beneficiary = %q, ожидался partner", items[0].Beneficiary)
	}
	if items[0].CategoryID != nil {
		t.Errorf("категория = %v, у перевода её быть не должно", *items[0].CategoryID)
	}
	if len(items[0].Words) != 0 {
		t.Errorf("слова перевода не должны попадать в кэш: %v", items[0].Words)
	}
}

func TestValidateClampsDaysAgo(t *testing.T) {
	items := Validate(
		[]RawItem{
			raw("600", "лимонад", "Продукты", BenBoth, KindExpense, 999),
			raw("600", "лимонад", "Продукты", BenBoth, KindExpense, -5),
		},
		"600 лимонад", testCategories(), testPayer, quietLog())

	if len(items) != 2 {
		t.Fatalf("ожидались два элемента, получено %d", len(items))
	}
	if items[0].DaysAgo != 30 {
		t.Errorf("days_ago = %d, ожидалось 30", items[0].DaysAgo)
	}
	if items[1].DaysAgo != 0 {
		t.Errorf("days_ago = %d, ожидалось 0", items[1].DaysAgo)
	}
}

func TestValidateBadEnumsFallBack(t *testing.T) {
	// Мусор в beneficiary заменяется умолчанием категории — у Продуктов both.
	items := Validate(
		[]RawItem{raw("600", "лимонад", "Продукты", "нам обоим", "покупка", 0)},
		"600 лимонад", testCategories(), testPayer, quietLog())

	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	if items[0].Beneficiary != BenBoth {
		t.Errorf("beneficiary = %q, ожидалось умолчание Продуктов", items[0].Beneficiary)
	}
	if items[0].Kind != KindExpense {
		t.Errorf("kind = %q, ожидался expense", items[0].Kind)
	}

	// А если у категории умолчания нет — безопасное «на себя».
	noDefault := []storage.Category{{ID: 1, Name: "Продукты"}, {ID: 3, Name: "Прочее"}}
	items = Validate(
		[]RawItem{raw("600", "лимонад", "Продукты", "нам обоим", KindExpense, 0)},
		"600 лимонад", noDefault, testPayer, quietLog())
	if items[0].Beneficiary != BenPayer {
		t.Errorf("beneficiary = %q, без умолчания ожидался payer", items[0].Beneficiary)
	}
}

func TestValidateEmptyDescriptionTakenFromText(t *testing.T) {
	items := Validate(
		[]RawItem{raw("600", "", "Продукты", BenBoth, KindExpense, 0)},
		"600 лимонад", testCategories(), testPayer, quietLog())

	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	if items[0].Description != "лимонад" {
		t.Errorf("описание = %q, ожидалось «лимонад»", items[0].Description)
	}
}

func TestValidateDropsNonPositiveAmount(t *testing.T) {
	items := Validate(
		[]RawItem{raw("0", "тест", "Продукты", BenBoth, KindExpense, 0)},
		"0 тест", testCategories(), testPayer, quietLog())

	if len(items) != 0 {
		t.Fatalf("нулевая сумма должна быть отброшена, получено %d", len(items))
	}
}

func TestValidateEmptyItemsStayEmpty(t *testing.T) {
	// Пустой ответ модели при непустых токенах — сигнал для деградированного
	// пути (§8), Validate ничего не досочиняет.
	items := Validate(nil, "600 лимонад", testCategories(), testPayer, quietLog())
	if len(items) != 0 {
		t.Fatalf("ожидался пустой результат, получено %d", len(items))
	}
}

func TestValidateLongDescriptionTrimmed(t *testing.T) {
	long := "оченьдлинноеописаниетратыкотороемодельзачемтопридумалаиононевлезаетвполе"
	items := Validate(
		[]RawItem{raw("600", long, "Продукты", BenBoth, KindExpense, 0)},
		"600 лимонад", testCategories(), testPayer, quietLog())

	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	if n := len([]rune(items[0].Description)); n > 64 {
		t.Errorf("описание длиной %d символов, максимум 64", n)
	}
}

func TestCategoryDefaultBeatsModelGuess(t *testing.T) {
	// Про получателя в сообщении не сказано — берём умолчание категории,
	// а не догадку модели: свои привычки люди знают лучше.
	item := raw("600", "лимонад", "Продукты", BenPayer, KindExpense, 0)
	item.BeneficiaryStated = false

	items := Validate([]RawItem{item}, "600 лимонад", testCategories(), testPayer, quietLog())
	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	if items[0].Beneficiary != BenBoth {
		t.Errorf("beneficiary = %q, у Продуктов умолчание both", items[0].Beneficiary)
	}
}

func TestStatedBeneficiaryWins(t *testing.T) {
	// А если сказано прямо — умолчание не вмешивается.
	item := raw("2500", "цветы", "Продукты", BenPartner, KindExpense, 0)
	item.BeneficiaryStated = true

	items := Validate([]RawItem{item}, "купил ей цветы 2500", testCategories(), testPayer, quietLog())
	if items[0].Beneficiary != BenPartner {
		t.Errorf("beneficiary = %q, в сообщении сказано «ей»", items[0].Beneficiary)
	}
}
