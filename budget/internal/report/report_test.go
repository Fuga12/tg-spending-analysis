package report

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// Группа из пяти человек. member_id и telegram id намеренно разные: отчёт
// работает с участниками, а не с аккаунтами.
var five = []storage.Member{
	{ID: 11, UserID: 1, Name: "Илья"},
	{ID: 12, UserID: 2, Name: "Аня"},
	{ID: 13, UserID: 3, Name: "Уля"},
	{ID: 14, UserID: 4, Name: "Дима"},
	{ID: 15, UserID: 5, Name: "Мила"},
}

func row(payer int64, amount, category string, recipients ...int64) storage.ExpenseRow {
	return storage.ExpenseRow{
		PayerMemberID: payer, Recipients: recipients,
		Amount: dec(amount), CategoryName: category,
	}
}

func byName(lines []Line) map[string]string {
	out := map[string]string{}
	for _, l := range lines {
		out[l.Name] = l.Amount.String()
	}
	return out
}

func TestBuildMonthFourBlocks(t *testing.T) {
	rows := []storage.ExpenseRow{
		row(11, "22100", "Продукты"),        // на всю группу
		row(11, "18000", "Жильё"),           // на всю группу
		row(11, "7100", "Такси", 11),        // Илья себе
		row(12, "9300", "Доставка еды", 12), // Аня себе
		row(12, "5000", "Подарки", 11),      // Аня — Илье
		row(11, "2000", "Одежда", 13),       // Илья — Уле
		row(14, "1200", "Здоровье", 14, 15), // Дима — себе и Миле
	}
	m := BuildMonth(2026, time.July, rows, five)

	// Блок 1 — итого.
	if !m.Total.Equal(dec("64700")) {
		t.Fatalf("всего = %s, ожидалось 64700", m.Total)
	}

	// Блок 2 — по категориям, по убыванию, с долями.
	if len(m.Categories) != 7 {
		t.Fatalf("категорий в отчёте %d, ожидалось 7", len(m.Categories))
	}
	if m.Categories[0].Name != "Продукты" || !m.Categories[0].Amount.Equal(dec("22100")) {
		t.Errorf("первая категория = %+v, ожидались Продукты 22100", m.Categories[0])
	}
	if m.Categories[0].Percent != 34 {
		t.Errorf("доля Продуктов = %d%%, ожидалось 34%%", m.Categories[0].Percent)
	}

	// Блок 3 — кто платил.
	payers := byName(m.Payers)
	if payers["Илья"] != "49200" || payers["Аня"] != "14300" || payers["Дима"] != "1200" {
		t.Errorf("кто платил = %+v, ожидались Илья 49200, Аня 14300, Дима 1200", payers)
	}
	if m.Payers[0].Name != "Илья" {
		t.Errorf("сверху блока = %q, ожидался Илья с самой большой суммой", m.Payers[0].Name)
	}
	if m.Payers[0].ID != 11 {
		t.Errorf("ID строки = %d, ожидался member_id 11", m.Payers[0].ID)
	}

	// Блок 4 — на кого ушло. Траты на всю группу стоят отдельной строкой и
	// по людям не раскладываются.
	got := byName(m.Beneficiaries)
	want := map[string]string{
		"Илья":  "12100", // 7100 своих + 5000 от Ани
		"Аня":   "9300",
		"Уля":   "2000",
		"Дима":  "600", // половина от 1200
		"Мила":  "600",
		"Общее": "40100", // 22100 + 18000 на всю группу
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

func TestSplitBetweenRecipientsKeepsTotal(t *testing.T) {
	// 1000 на троих не делится нацело. Остаток обязан остаться в отчёте:
	// копейка, потерянная на округлении, — это расхождение блока с итогом.
	m := BuildMonth(2026, time.July, []storage.ExpenseRow{
		row(11, "1000", "Продукты", 11, 12, 13),
	}, five)

	sum := decimal.Zero
	for _, l := range m.Beneficiaries {
		sum = sum.Add(l.Amount)
	}
	if !sum.Equal(dec("1000")) {
		t.Errorf("сумма долей = %s, ожидалось ровно 1000 (%+v)", sum, byName(m.Beneficiaries))
	}
	got := byName(m.Beneficiaries)
	if got["Илья"] != "333.34" || got["Аня"] != "333.33" || got["Уля"] != "333.33" {
		t.Errorf("доли = %+v, ожидались 333.34 / 333.33 / 333.33", got)
	}
}

func TestBuildMonthNoCategory(t *testing.T) {
	m := BuildMonth(2026, time.July, []storage.ExpenseRow{
		row(11, "600", "", 11),
	}, five)

	if len(m.Categories) != 1 || m.Categories[0].Name != NoCategory {
		t.Errorf("категории = %+v, ожидалась «%s»", m.Categories, NoCategory)
	}
}

func TestBuildMonthEmpty(t *testing.T) {
	m := BuildMonth(2026, time.July, nil, five)
	if !m.Total.IsZero() || len(m.Categories) != 0 {
		t.Errorf("пустой месяц = %+v", m)
	}
}

func TestFormerMemberKeepsHisName(t *testing.T) {
	// Человек вышел из группы, но его траты остались в истории. Подписать их
	// именем всё равно надо — отсюда AllMembers у отчёта.
	left := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	members := append([]storage.Member{}, five...)
	members[2].LeftAt = &left

	m := BuildMonth(2026, time.July, []storage.ExpenseRow{
		row(13, "500", "Такси", 13),
	}, members)

	if len(m.Payers) != 1 || m.Payers[0].Name != "Уля" {
		t.Errorf("кто платил = %+v, ожидалась Уля", m.Payers)
	}
}

func TestUnknownMemberDoesNotLoseMoney(t *testing.T) {
	// Участника в списке нет — такого быть не должно, но потерять деньги
	// из отчёта хуже, чем показать их без имени.
	m := BuildMonth(2026, time.July, []storage.ExpenseRow{
		row(999, "5000", "Подарки", 999),
	}, five)

	sum := decimal.Zero
	for _, l := range m.Beneficiaries {
		sum = sum.Add(l.Amount)
	}
	if !sum.Equal(dec("5000")) {
		t.Errorf("сумма блока = %s, ожидалось 5000", sum)
	}
	if len(m.Beneficiaries) != 1 || m.Beneficiaries[0].Name != UnknownMember {
		t.Errorf("строка = %+v, ожидалась «%s»", m.Beneficiaries, UnknownMember)
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
