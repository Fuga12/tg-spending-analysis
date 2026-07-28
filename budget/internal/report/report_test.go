package report

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var (
	ilya = storage.User{ID: 1, Name: "Илья"}
	anya = storage.User{ID: 2, Name: "Аня"}
)

func row(payer int64, beneficiary, amount, category string) storage.ExpenseRow {
	return storage.ExpenseRow{
		PayerID: payer, Beneficiary: beneficiary,
		Amount: dec(amount), CategoryName: category,
	}
}

func TestBuildMonthFourBlocks(t *testing.T) {
	rows := []storage.ExpenseRow{
		row(1, classify.BenBoth, "22100", "Продукты"),
		row(1, classify.BenBoth, "18000", "Жильё"),
		row(1, classify.BenPayer, "7100", "Такси"),
		row(2, classify.BenPayer, "9300", "Доставка еды"),
		row(2, classify.BenPartner, "5000", "Подарки"),
		row(1, classify.BenPartner, "2000", "Одежда"),
	}
	m := BuildMonth(2026, time.July, rows, []storage.User{ilya, anya})

	// Блок 1 — итого.
	if !m.Total.Equal(dec("63500")) {
		t.Fatalf("всего = %s, ожидалось 63500", m.Total)
	}

	// Блок 2 — по категориям, по убыванию, с долями.
	if len(m.Categories) != 6 {
		t.Fatalf("категорий в отчёте %d, ожидалось 6", len(m.Categories))
	}
	if m.Categories[0].Name != "Продукты" || !m.Categories[0].Amount.Equal(dec("22100")) {
		t.Errorf("первая категория = %+v, ожидались Продукты 22100", m.Categories[0])
	}
	if m.Categories[0].Percent != 35 {
		t.Errorf("доля Продуктов = %d%%, ожидалось 35%%", m.Categories[0].Percent)
	}

	// Блок 3 — кто платил.
	if len(m.Payers) != 2 || m.Payers[0].Name != "Илья" || !m.Payers[0].Amount.Equal(dec("49200")) {
		t.Errorf("кто платил = %+v, ожидался Илья с 49200 сверху", m.Payers)
	}

	// Блок 4 — на кого ушло: на себя, на партнёра от второго, общее отдельно.
	got := map[string]string{}
	for _, l := range m.Beneficiaries {
		got[l.Name] = l.Amount.String()
	}
	want := map[string]string{
		"Илья":  "12100", // 7100 своих + 5000, что Аня потратила на партнёра
		"Аня":   "11300", // 9300 своих + 2000 от Ильи
		"Общее": "40100", // 22100 + 18000 на двоих
	}
	for name, amount := range want {
		if got[name] != amount {
			t.Errorf("на кого ушло: %s = %s, ожидалось %s (всё: %+v)", name, got[name], amount, got)
		}
	}

	// Суммы блока 4 обязаны сходиться с итогом, иначе деньги пропали.
	sum := decimal.Zero
	for _, l := range m.Beneficiaries {
		sum = sum.Add(l.Amount)
	}
	if !sum.Equal(m.Total) {
		t.Errorf("сумма блока «на кого ушло» = %s, а всего %s", sum, m.Total)
	}
}

func TestBuildMonthNoCategory(t *testing.T) {
	m := BuildMonth(2026, time.July, []storage.ExpenseRow{
		row(1, classify.BenPayer, "600", ""),
	}, []storage.User{ilya, anya})

	if len(m.Categories) != 1 || m.Categories[0].Name != NoCategory {
		t.Errorf("категории = %+v, ожидалась «%s»", m.Categories, NoCategory)
	}
}

func TestBuildMonthEmpty(t *testing.T) {
	m := BuildMonth(2026, time.July, nil, []storage.User{ilya, anya})
	if !m.Total.IsZero() || len(m.Categories) != 0 {
		t.Errorf("пустой месяц = %+v", m)
	}
}

func TestPartnerUnknownGoesToCommon(t *testing.T) {
	// Пользователь пока один: «на партнёра» отнести некому, но деньги
	// из отчёта пропасть не должны.
	m := BuildMonth(2026, time.July, []storage.ExpenseRow{
		row(1, classify.BenPartner, "5000", "Подарки"),
	}, []storage.User{ilya})

	sum := decimal.Zero
	for _, l := range m.Beneficiaries {
		sum = sum.Add(l.Amount)
	}
	if !sum.Equal(dec("5000")) {
		t.Errorf("сумма блока = %s, ожидалось 5000", sum)
	}
}

func TestMonthRangeUsesTimezone(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	from, to := MonthRange(2026, time.July, msk)

	if from.Format(time.RFC3339) != "2026-07-01T00:00:00+03:00" {
		t.Errorf("начало месяца = %s", from.Format(time.RFC3339))
	}
	if to.Format(time.RFC3339) != "2026-08-01T00:00:00+03:00" {
		t.Errorf("конец месяца = %s", to.Format(time.RFC3339))
	}
	// В базу границы уезжают как UTC — это делает pgx, важно лишь, что момент
	// времени правильный.
	if from.UTC().Format(time.RFC3339) != "2026-06-30T21:00:00Z" {
		t.Errorf("начало месяца в UTC = %s", from.UTC().Format(time.RFC3339))
	}
}

func TestDayRange(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	from, to := DayRange(time.Date(2026, 7, 28, 23, 30, 0, 0, time.UTC), msk)

	// 23:30 UTC — это уже 29 июля по Москве.
	if from.Format("02.01") != "29.07" || to.Sub(from) != 24*time.Hour {
		t.Errorf("сутки = %s .. %s", from.Format("02.01 15:04"), to.Format("02.01 15:04"))
	}
}
