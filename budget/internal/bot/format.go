package bot

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

// nbsp — неразрывный пробел: «1 200 ₽» не должно рваться переносом.
const nbsp = " "

// Money форматирует сумму так, как её читают: «1 200 ₽», без копеек, если их нет.
func Money(v decimal.Decimal) string {
	s := v.StringFixed(2)
	s = strings.TrimSuffix(s, ".00")

	intPart, frac, hasFrac := strings.Cut(s, ".")
	neg := strings.HasPrefix(intPart, "-")
	intPart = strings.TrimPrefix(intPart, "-")

	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteString(nbsp)
		}
		b.WriteRune(c)
	}
	out := b.String()
	if hasFrac {
		out += "," + frac
	}
	if neg {
		out = "−" + out
	}
	return out + nbsp + "₽"
}

// dayLabel — «вчера», «позавчера» или дата, если трата не сегодняшняя.
// Пустая строка означает «сегодня», её показывать не нужно.
func dayLabel(spentAt, now time.Time, loc *time.Location) string {
	day := spentAt.In(loc)
	today := now.In(loc)

	diff := int(startOfDay(today).Sub(startOfDay(day)).Hours() / 24)
	switch {
	case diff == 0:
		return ""
	case diff == 1:
		return "вчера"
	case diff == 2:
		return "позавчера"
	default:
		return day.Format("02.01")
	}
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// transactionLine — подтверждение записанной траты.
//
// Получателя строка больше не называет: при группе до десяти человек он не
// умещается в подпись и выбирается в приложении. Задача ответа — показать,
// что трата записана и как понята сумма.
func transactionLine(t storage.Transaction, now time.Time, loc *time.Location) string {
	if t.Kind == classify.KindTransfer {
		return "↔ Перевод " + Money(t.Amount)
	}

	head := "✓ " + Money(t.Amount)
	if t.Kind == classify.KindIncome {
		// Поступление иначе читается как трата.
		head = "↑ " + Money(t.Amount)
	}
	parts := []string{head}

	if t.NeedsClassification {
		return strings.Join(append(parts, "категория позже"), " · ")
	}

	if t.CategoryName != "" {
		parts = append(parts, t.CategoryName)
	} else {
		parts = append(parts, "без категории")
	}
	if day := dayLabel(t.SpentAt, now, loc); day != "" {
		parts = append(parts, day)
	}
	return strings.Join(parts, " · ")
}
