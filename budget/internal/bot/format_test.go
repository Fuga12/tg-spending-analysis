package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var msk = time.FixedZone("MSK", 3*60*60)

func TestMoney(t *testing.T) {
	cases := map[string]string{
		"600":       "600" + nbsp + "₽",
		"1200":      "1" + nbsp + "200" + nbsp + "₽",
		"1200.50":   "1" + nbsp + "200,50" + nbsp + "₽",
		"90000":     "90" + nbsp + "000" + nbsp + "₽",
		"1234567":   "1" + nbsp + "234" + nbsp + "567" + nbsp + "₽",
		"0.05":      "0,05" + nbsp + "₽",
		"-1200":     "−1" + nbsp + "200" + nbsp + "₽",
		"1200.00":   "1" + nbsp + "200" + nbsp + "₽",
		"999":       "999" + nbsp + "₽",
		"1000":      "1" + nbsp + "000" + nbsp + "₽",
		"123456.78": "123" + nbsp + "456,78" + nbsp + "₽",
	}
	for in, want := range cases {
		if got := Money(dec(in)); got != want {
			t.Errorf("Money(%s) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestMoneyUsesNonBreakingSpaces(t *testing.T) {
	// «1 200 ₽» не должно рваться переносом строки посреди суммы.
	got := Money(dec("1200"))
	if strings.ContainsRune(got, ' ') {
		t.Errorf("в %q есть обычный пробел — сумма порвётся переносом", got)
	}
}

func TestDayLabel(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, msk)

	cases := []struct {
		spent time.Time
		want  string
		note  string
	}{
		{now, "", "сегодня показывать незачем"},
		{now.Add(-11 * time.Hour), "", "то же число, другое время суток"},
		{now.AddDate(0, 0, -1), "вчера", "вчера"},
		{now.AddDate(0, 0, -2), "позавчера", "позавчера"},
		{now.AddDate(0, 0, -3), "25.07", "дальше — дата"},
		{now.AddDate(0, -1, 0), "28.06", "прошлый месяц"},
	}
	for _, c := range cases {
		if got := dayLabel(c.spent, now, msk); got != c.want {
			t.Errorf("%s: dayLabel = %q, ожидалось %q", c.note, got, c.want)
		}
	}
}

func TestDayLabelCountsDaysInBotTimezone(t *testing.T) {
	// 23:30 UTC — это уже следующий день по Москве. Резать по UTC значит
	// подписать сегодняшнюю трату вчерашней.
	now := time.Date(2026, 7, 28, 23, 30, 0, 0, time.UTC)
	spent := time.Date(2026, 7, 29, 0, 10, 0, 0, msk)

	if got := dayLabel(spent, now, msk); got != "" {
		t.Errorf("dayLabel = %q, ожидалось пусто — по Москве это один день", got)
	}
}

func TestTransactionLine(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, msk)

	cases := []struct {
		name string
		tx   storage.Transaction
		want string
	}{
		{
			name: "обычная трата",
			tx: storage.Transaction{
				Kind: classify.KindExpense, Amount: dec("600"),
				CategoryName: "Продукты", SpentAt: now,
			},
			want: "✓ 600 ₽ · Продукты",
		},
		{
			name: "вчерашняя трата подписана днём",
			tx: storage.Transaction{
				Kind: classify.KindExpense, Amount: dec("1200"),
				CategoryName: "Продукты", SpentAt: now.AddDate(0, 0, -1),
			},
			want: "✓ 1 200 ₽ · Продукты · вчера",
		},
		{
			name: "без категории",
			tx: storage.Transaction{
				Kind: classify.KindExpense, Amount: dec("450"), SpentAt: now,
			},
			want: "✓ 450 ₽ · без категории",
		},
		{
			name: "категорию доберёт воркер",
			tx: storage.Transaction{
				Kind: classify.KindExpense, Amount: dec("600"),
				NeedsClassification: true, SpentAt: now,
			},
			want: "✓ 600 ₽ · категория позже",
		},
		{
			name: "поступление читается иначе, чем трата",
			tx: storage.Transaction{
				Kind: classify.KindIncome, Amount: dec("90000"),
				CategoryName: "Прочее", SpentAt: now,
			},
			want: "↑ 90 000 ₽ · Прочее",
		},
		{
			name: "перевод — не трата и не категория",
			tx: storage.Transaction{
				Kind: classify.KindTransfer, Amount: dec("5000"), SpentAt: now,
			},
			want: "↔ Перевод 5 000 ₽",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := transactionLine(c.tx, now, msk)
			// В коде неразрывные пробелы, в ожидании — обычные: так строку
			// видно глазами.
			if readable(got) != c.want {
				t.Errorf("строка = %q, ожидалось %q", readable(got), c.want)
			}
		})
	}
}

func TestTransactionLineSaysNothingAboutRecipients(t *testing.T) {
	// При группе до десяти человек получатель в подпись не умещается и
	// выбирается в приложении. Задача ответа — показать, что трата записана
	// и как понята сумма.
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, msk)
	line := transactionLine(storage.Transaction{
		Kind: classify.KindExpense, Amount: dec("600"),
		CategoryName: "Продукты", Recipients: []int64{11, 12},
		SpentAt: now,
	}, now, msk)

	if readable(line) != "✓ 600 ₽ · Продукты" {
		t.Errorf("строка = %q, получателей в ней быть не должно", readable(line))
	}
}

// readable заменяет неразрывные пробелы обычными — только для сообщений
// об ошибках и сравнения в тестах.
func readable(s string) string { return strings.ReplaceAll(s, nbsp, " ") }
