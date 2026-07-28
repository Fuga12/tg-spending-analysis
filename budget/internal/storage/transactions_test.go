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

	if err := s.UpsertUser(ctx, 1, "Илья"); err != nil {
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

func TestOnlyPayerCanEditTransaction(t *testing.T) {
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

	// Второй пользователь трогать чужую трату не может (§9).
	for _, check := range []struct {
		name string
		call func() (bool, error)
	}{
		{"бенефициар", func() (bool, error) { return s.SetBeneficiary(ctx, id, 2, "both") }},
		{"категория", func() (bool, error) { return s.SetCategory(ctx, id, 2, taxi) }},
		{"удаление", func() (bool, error) { return s.DeleteTransaction(ctx, id, 2) }},
	} {
		ok, err := check.call()
		if err != nil {
			t.Fatalf("%s: %v", check.name, err)
		}
		if ok {
			t.Errorf("%s: чужую трату менять нельзя", check.name)
		}
	}

	// Плательщику — можно.
	if ok, err := s.SetBeneficiary(ctx, id, 1, "both"); err != nil || !ok {
		t.Fatalf("плательщик не смог сменить бенефициара: ok=%v err=%v", ok, err)
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

	if ok, err := s.SetCategory(ctx, id, 1, food); err != nil || !ok {
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

	if ok, err := s.DeleteTransaction(ctx, id, 1); err != nil || !ok {
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
	if ok, _ := s.DeleteTransaction(ctx, id, 1); ok {
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
	if err := s.UpsertUser(context.Background(), id, "Тест"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
}
