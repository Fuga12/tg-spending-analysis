package storage

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestInsertAndReadTransaction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.EnsureUser(ctx, 1, "Тест"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	cats, _ := s.Categories(ctx)
	food := categoryID(t, cats, "Продукты")

	spent := time.Now().AddDate(0, 0, -1)
	id, err := s.InsertTransaction(ctx, Transaction{
		PayerID:     1,
		Beneficiary: "both",
		Kind:        "expense",
		Amount:      decimal.RequireFromString("1200.50"),
		Description: "пятёрочка",
		CategoryID:  &food,
		RawText:     "вчера пятёрочка 1200,50",
		SpentAt:     spent,
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	tx, err := s.Transaction(ctx, id)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if !tx.Amount.Equal(decimal.RequireFromString("1200.50")) {
		t.Errorf("сумма = %s, ожидалось 1200.50", tx.Amount)
	}
	if tx.CategoryName != "Продукты" {
		t.Errorf("категория = %q, ожидались Продукты", tx.CategoryName)
	}
	if tx.SpentAt.Sub(spent).Abs() > time.Second {
		t.Errorf("дата траты = %s, ожидалась %s", tx.SpentAt, spent)
	}
	if tx.NeedsClassification {
		t.Error("флаг «разобрать позже» не должен стоять")
	}
}

func TestDegradedTransactionHasNoCategory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	id, err := s.InsertTransaction(ctx, Transaction{
		PayerID:             1,
		Beneficiary:         "payer",
		Kind:                "expense",
		Amount:              decimal.RequireFromString("600"),
		Description:         "лимонад",
		RawText:             "600 лимонад",
		NeedsClassification: true,
		SpentAt:             time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка деградированной записи: %v", err)
	}

	tx, err := s.Transaction(ctx, id)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if tx.CategoryID != nil || !tx.NeedsClassification {
		t.Errorf("запись = %+v, ожидались пустая категория и флаг разбора", tx)
	}
}

func TestAnyoneCanEditTransaction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)
	mustUser(t, s, 2)

	cats, _ := s.Categories(ctx)
	taxi := categoryID(t, cats, "Такси")

	id, err := s.InsertTransaction(ctx, Transaction{
		PayerID: 1, Beneficiary: "payer", Kind: "expense",
		Amount: decimal.RequireFromString("450"), Description: "такси",
		RawText: "такси 450", SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	// Бюджет общий: править запись партнёра разрешено обоим.
	for _, check := range []struct {
		name string
		call func() (bool, error)
	}{
		{"бенефициар", func() (bool, error) { return s.SetBeneficiary(ctx, id, "both") }},
		{"категория", func() (bool, error) { return s.SetCategory(ctx, id, taxi) }},
	} {
		ok, err := check.call()
		if err != nil {
			t.Fatalf("%s: %v", check.name, err)
		}
		if !ok {
			t.Errorf("%s: править чужую трату разрешено", check.name)
		}
	}
	tx, _ := s.Transaction(ctx, id)
	if tx.Beneficiary != "both" {
		t.Errorf("бенефициар = %q, ожидался both", tx.Beneficiary)
	}
}

func TestSetCategoryClearsNeedsClassification(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	cats, _ := s.Categories(ctx)
	food := categoryID(t, cats, "Продукты")

	id, _ := s.InsertTransaction(ctx, Transaction{
		PayerID: 1, Beneficiary: "payer", Kind: "expense",
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", NeedsClassification: true, SpentAt: time.Now(),
	})

	if ok, err := s.SetCategory(ctx, id, food); err != nil || !ok {
		t.Fatalf("смена категории: ok=%v err=%v", ok, err)
	}
	tx, _ := s.Transaction(ctx, id)
	if tx.NeedsClassification {
		t.Error("после ручной категории флаг «разобрать позже» должен сниматься")
	}
}

func TestDeleteIsSoftAndHidesTransaction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	id, _ := s.InsertTransaction(ctx, Transaction{
		PayerID: 1, Beneficiary: "payer", Kind: "expense",
		Amount: decimal.RequireFromString("450"), Description: "такси",
		RawText: "такси 450", SpentAt: time.Now(),
	})

	if ok, err := s.DeleteTransaction(ctx, id); err != nil || !ok {
		t.Fatalf("удаление: ok=%v err=%v", ok, err)
	}
	if _, err := s.Transaction(ctx, id); err == nil {
		t.Error("удалённая трата не должна читаться")
	}

	// Но строка осталась: удаление только мягкое (§3).
	var deleted int
	if err := s.pool.QueryRow(ctx,
		`select count(*) from transactions where id = $1 and deleted_at is not null`, id).Scan(&deleted); err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if deleted != 1 {
		t.Error("строка должна остаться в базе с проставленным deleted_at")
	}

	// Повторное удаление ничего не меняет.
	if ok, _ := s.DeleteTransaction(ctx, id); ok {
		t.Error("повторное удаление не должно проходить")
	}
}

func TestAmountMustBePositive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	_, err := s.InsertTransaction(ctx, Transaction{
		PayerID: 1, Beneficiary: "payer", Kind: "expense",
		Amount: decimal.Zero, Description: "тест", RawText: "0 тест", SpentAt: time.Now(),
	})
	if err == nil {
		t.Error("нулевая сумма не должна проходить check (amount > 0)")
	}
}

func mustUser(t *testing.T, s *Store, id int64) {
	t.Helper()
	if err := s.EnsureUser(context.Background(), id, "Тест"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
}

func TestExpensesFiltersPeriodKindAndDeleted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	now := time.Now()
	insert := func(kind string, amount string, spentAt time.Time, deleted bool) int64 {
		t.Helper()
		id, err := s.InsertTransaction(ctx, Transaction{
			PayerID: 1, Beneficiary: "both", Kind: kind,
			Amount: decimal.RequireFromString(amount), Description: "тест",
			RawText: "тест", SpentAt: spentAt,
		})
		if err != nil {
			t.Fatalf("вставка: %v", err)
		}
		if deleted {
			if _, err := s.DeleteTransaction(ctx, id); err != nil {
				t.Fatalf("удаление: %v", err)
			}
		}
		return id
	}

	insert("expense", "1000", now, false)
	insert("expense", "2000", now, true)                    // удалённая
	insert("transfer", "5000", now, false)                  // перевод — не расход
	insert("income", "90000", now, false)                   // доход — не расход
	insert("expense", "700", now.AddDate(0, 0, -40), false) // другой месяц

	from := now.AddDate(0, 0, -7)
	to := now.AddDate(0, 0, 1)
	rows, err := s.Expenses(ctx, from, to)
	if err != nil {
		t.Fatalf("расходы: %v", err)
	}
	if len(rows) != 1 || !rows[0].Amount.Equal(decimal.RequireFromString("1000")) {
		t.Fatalf("строк %d (%+v), ожидалась одна на 1000 — переводы, доходы, удалённые и чужие месяцы не в счёт", len(rows), rows)
	}
}

func TestPendingClassification(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	cats, _ := s.Categories(ctx)
	food := categoryID(t, cats, "Продукты")

	id, _ := s.InsertTransaction(ctx, Transaction{
		PayerID: 1, Beneficiary: "payer", Kind: "expense",
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", NeedsClassification: true, SpentAt: time.Now(),
	})
	_, _ = s.InsertTransaction(ctx, Transaction{
		PayerID: 1, Beneficiary: "payer", Kind: "expense",
		Amount: decimal.RequireFromString("450"), Description: "такси",
		RawText: "такси 450", CategoryID: &food, SpentAt: time.Now(),
	})

	pending, err := s.PendingClassification(ctx, 20)
	if err != nil {
		t.Fatalf("выборка: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("добирать нужно только помеченные записи, получено %+v", pending)
	}
	if pending[0].RawText != "600 лимонад" {
		t.Errorf("raw_text = %q — воркеру нужен исходный текст", pending[0].RawText)
	}

	// После простановки категории запись из очереди уходит.
	if ok, err := s.SetCategory(ctx, id, food); err != nil || !ok {
		t.Fatalf("простановка категории: ok=%v err=%v", ok, err)
	}
	if pending, _ = s.PendingClassification(ctx, 20); len(pending) != 0 {
		t.Errorf("в очереди осталось %d записей, ожидалось 0", len(pending))
	}
}
