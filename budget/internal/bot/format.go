package bot

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/report"
	"budget/internal/storage"
)

// nbsp — неразрывный пробел: «1 200 ₽» не должно рваться переносом.
const nbsp = " "

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

// beneficiaryLabel — подпись, на кого потрачено.
func beneficiaryLabel(b string) string {
	switch b {
	case classify.BenPayer:
		return "👤 на себя"
	case classify.BenPartner:
		return "🧍 на неё"
	default:
		return "👥 на двоих"
	}
}

// beneficiaryIcon — короткая форма: когда в строке есть ещё и дата, места
// на слова не остаётся (§9).
func beneficiaryIcon(b string) string {
	switch b {
	case classify.BenPayer:
		return "👤"
	case classify.BenPartner:
		return "🧍"
	default:
		return "👥"
	}
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

// transactionLine — строка ответа на записанную трату (§9).
func transactionLine(t storage.Transaction, now time.Time, loc *time.Location) string {
	if t.Kind == classify.KindTransfer {
		return "↔ Перевод " + Money(t.Amount)
	}

	parts := []string{"✓ " + Money(t.Amount)}
	if t.Kind == classify.KindIncome {
		// Поступление иначе читается как трата. В плане такой строки нет,
		// но и дохода без пометки там тоже нет.
		parts[0] = "↑ " + Money(t.Amount)
	}

	if t.NeedsClassification {
		// Ровно как в §8: «✓ 600 ₽ · категория позже», без бенефициара.
		return strings.Join(append(parts, "категория позже"), " · ")
	}

	if t.CategoryName != "" {
		parts = append(parts, t.CategoryName)
	} else {
		parts = append(parts, "без категории")
	}

	day := dayLabel(t.SpentAt, now, loc)
	if day == "" {
		parts = append(parts, beneficiaryLabel(t.Beneficiary))
	} else {
		parts = append(parts, beneficiaryIcon(t.Beneficiary), day)
	}
	return strings.Join(parts, " · ")
}

// monthNames — родительный падеж не нужен: заголовок отчёта именительный.
var monthNames = [...]string{
	"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
	"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь",
}

// formatMonth рисует четыре блока отчёта (§10).
func formatMonth(m report.Month) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 %s %d\n\n", monthNames[int(m.Month)-1], m.Year)

	if m.Total.IsZero() {
		sb.WriteString("Трат за этот месяц нет.")
		return sb.String()
	}

	fmt.Fprintf(&sb, "Всего: %s\n", Money(m.Total))

	sb.WriteString("\nПо категориям\n")
	for _, l := range m.Categories {
		fmt.Fprintf(&sb, "  %s%s  %d%%\n", pad(l.Name, 20), padLeft(Money(l.Amount), 12), l.Percent)
	}

	sb.WriteString("\nКто платил\n")
	for _, l := range m.Payers {
		fmt.Fprintf(&sb, "  %s%s\n", pad(l.Name, 20), padLeft(Money(l.Amount), 12))
	}

	sb.WriteString("\nНа кого ушло\n")
	for _, l := range m.Beneficiaries {
		fmt.Fprintf(&sb, "  %s%s\n", pad(l.Name, 20), padLeft(Money(l.Amount), 12))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatDay — траты за сегодня списком (§9).
func formatDay(txs []storage.Transaction, users []storage.User, now time.Time, loc *time.Location) string {
	names := make(map[int64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Name
	}

	total := decimal.Zero
	var sb strings.Builder
	sb.WriteString("📅 Сегодня\n\n")

	for _, t := range txs {
		who := names[t.PayerID]
		if who == "" {
			who = "кто-то"
		}
		if t.Kind == classify.KindExpense {
			total = total.Add(t.Amount)
		}
		fmt.Fprintf(&sb, "  %s · %s · %s\n", transactionLine(t, now, loc), descriptionOr(t), who)
	}

	fmt.Fprintf(&sb, "\nИтого расходов: %s", Money(total))
	return sb.String()
}

func descriptionOr(t storage.Transaction) string {
	if t.Description == "" {
		return "без описания"
	}
	return t.Description
}

// statsView — доля быстрого пути, см. classify.Stats.
type statsView = classify.Stats

// formatUsage — ответ команды /лимит (§7).
func formatUsage(v usageView) string {
	var sb strings.Builder
	sb.WriteString("🔢 Расход токенов за месяц\n\n")

	percent := 0
	if v.Limit > 0 {
		percent = int(float64(v.Used.TotalTokens) / float64(v.Limit) * 100)
	}
	rub := float64(v.Used.TotalTokens) / 1000 * v.PricePer1K
	fmt.Fprintf(&sb, "  %d из %d токенов — %d%%, примерно %.2f ₽\n",
		v.Used.TotalTokens, v.Limit, percent, rub)
	fmt.Fprintf(&sb, "  вызовов API: %d, неуспешных: %d\n", v.Used.Calls, v.Used.Failed)

	if len(v.Errors) > 0 {
		kinds := make([]string, 0, len(v.Errors))
		for kind := range v.Errors {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			fmt.Fprintf(&sb, "    %s: %d\n", kind, v.Errors[kind])
		}
	}

	total := v.Stats.Total()
	if total > 0 {
		fmt.Fprintf(&sb, "\nБез обращения к API: %d%% сообщений (%d из %d с момента запуска)\n",
			int(float64(v.Stats.Cache)/float64(total)*100), v.Stats.Cache, total)
		if v.Stats.Degraded > 0 {
			fmt.Fprintf(&sb, "Записано без категории: %d\n", v.Stats.Degraded)
		}
	}

	if v.BreakerOpen {
		fmt.Fprintf(&sb, "\n⚠️ Breaker открыт до %s — в сеть не ходим", v.BreakerTill.Format("15:04"))
	} else {
		sb.WriteString("\nBreaker закрыт, API доступен")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// pad и padLeft выравнивают колонки отчёта по ширине в символах.
func pad(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s + " "
}

func padLeft(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}
