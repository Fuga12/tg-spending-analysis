package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

func TestMoney(t *testing.T) {
	cases := []struct{ in, want string }{
		{"600", "600 ₽"},
		{"1200", "1 200 ₽"},
		{"1200.50", "1 200,50 ₽"},
		{"68400", "68 400 ₽"},
		{"1000000", "1 000 000 ₽"},
		{"0.50", "0,50 ₽"},
	}
	for _, c := range cases {
		got := Money(decimal.RequireFromString(c.in))
		// Сравниваем с обычными пробелами: в выводе они неразрывные.
		if strings.ReplaceAll(got, nbsp, " ") != c.want {
			t.Errorf("Money(%s) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestDayLabel(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 7, 28, 21, 30, 0, 0, loc)

	cases := []struct {
		spent time.Time
		want  string
	}{
		{time.Date(2026, 7, 28, 1, 0, 0, 0, loc), ""},
		{time.Date(2026, 7, 27, 23, 0, 0, 0, loc), "вчера"},
		{time.Date(2026, 7, 26, 12, 0, 0, 0, loc), "позавчера"},
		{time.Date(2026, 7, 20, 12, 0, 0, 0, loc), "20.07"},
	}
	for _, c := range cases {
		if got := dayLabel(c.spent, now, loc); got != c.want {
			t.Errorf("dayLabel(%s) = %q, ожидалось %q", c.spent.Format("02.01 15:04"), got, c.want)
		}
	}
}

func TestTransactionLine(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 7, 28, 21, 0, 0, 0, loc)

	cases := []struct {
		name string
		tx   storage.Transaction
		want string
	}{
		{
			name: "обычная трата",
			tx: storage.Transaction{
				Amount: decimal.RequireFromString("1200"), CategoryName: "Доставка еды",
				Beneficiary: classify.BenBoth, Kind: classify.KindExpense, SpentAt: now,
			},
			want: "✓ 1 200 ₽ · Доставка еды · 👥 на двоих",
		},
		{
			name: "вчерашняя трата",
			tx: storage.Transaction{
				Amount: decimal.RequireFromString("1200"), CategoryName: "Продукты",
				Beneficiary: classify.BenBoth, Kind: classify.KindExpense,
				SpentAt: now.AddDate(0, 0, -1),
			},
			want: "✓ 1 200 ₽ · Продукты · 👥 · вчера",
		},
		{
			name: "деградированная запись",
			tx: storage.Transaction{
				Amount: decimal.RequireFromString("600"), Beneficiary: classify.BenPayer,
				Kind: classify.KindExpense, NeedsClassification: true, SpentAt: now,
			},
			want: "✓ 600 ₽ · категория позже",
		},
		{
			name: "перевод",
			tx: storage.Transaction{
				Amount: decimal.RequireFromString("5000"), Beneficiary: classify.BenPartner,
				Kind: classify.KindTransfer, SpentAt: now,
			},
			want: "↔ Перевод 5 000 ₽",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.ReplaceAll(transactionLine(c.tx, now, loc), nbsp, " ")
			if got != c.want {
				t.Errorf("строка = %q, ожидалась %q", got, c.want)
			}
		})
	}
}

func TestKeyboards(t *testing.T) {
	m := transactionKeyboard(42)
	if len(m.InlineKeyboard) != 2 {
		t.Fatalf("рядов клавиатуры %d, ожидалось два", len(m.InlineKeyboard))
	}
	if len(m.InlineKeyboard[0]) != 3 || len(m.InlineKeyboard[1]) != 2 {
		t.Errorf("раскладка = %d и %d кнопок, ожидалось 3 и 2",
			len(m.InlineKeyboard[0]), len(m.InlineKeyboard[1]))
	}
	if m.InlineKeyboard[0][0].Data != "42" {
		t.Errorf("данные кнопки = %q, ожидался id транзакции", m.InlineKeyboard[0][0].Data)
	}

	// У перевода менять нечего, кроме факта его существования (§9).
	tr := transferKeyboard(42)
	if len(tr.InlineKeyboard) != 1 || len(tr.InlineKeyboard[0]) != 1 {
		t.Errorf("клавиатура перевода = %v, ожидалась одна кнопка удаления", tr.InlineKeyboard)
	}

	// Категории — по две в ряд.
	cats := []storage.Category{{ID: 1, Name: "Продукты"}, {ID: 2, Name: "Такси"}, {ID: 3, Name: "Прочее"}}
	ck := categoryKeyboard(42, cats)
	if len(ck.InlineKeyboard) != 2 {
		t.Fatalf("рядов категорий %d, ожидалось два", len(ck.InlineKeyboard))
	}
	if len(ck.InlineKeyboard[0]) != 2 || len(ck.InlineKeyboard[1]) != 1 {
		t.Errorf("раскладка категорий = %d и %d", len(ck.InlineKeyboard[0]), len(ck.InlineKeyboard[1]))
	}
	if ck.InlineKeyboard[0][1].Data != "42:2" {
		t.Errorf("данные кнопки категории = %q, ожидалось «42:2»", ck.InlineKeyboard[0][1].Data)
	}
}

func TestCommandParsing(t *testing.T) {
	// telebot не считает «/месяц 6» командой: кириллица не подходит под его
	// регулярку. Разбираем сами — иначе это сообщение записалось бы тратой
	// на 6 ₽ и сожгло токены на разбор.
	cases := []struct{ in, name, payload string }{
		{"/месяц 6", "/месяц", "6"},
		{"/месяц", "/месяц", ""},
		{"/МЕСЯЦ 6", "/месяц", "6"},
		{"/месяц@budget_bot 6", "/месяц", "6"},
		{"/month 6", "/month", "6"},
		{"/день", "/день", ""},
	}
	for _, c := range cases {
		name, payload := parseCommand(c.in)
		if name != c.name || payload != c.payload {
			t.Errorf("%q → команда %q, аргумент %q; ожидались %q и %q",
				c.in, name, payload, c.name, c.payload)
		}
	}
}
