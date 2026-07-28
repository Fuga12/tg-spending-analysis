// Package report считает месячный отчёт: четыре блока из §10.
//
// SQL сознательно простой — из базы приезжают строки (payer_id, beneficiary,
// amount), а раскладка «на кого ушло» считается здесь.
package report

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

// NoCategory — как называется отсутствующая категория в отчёте (§10).
const NoCategory = "Без категории"

// CommonBucket — доля трат на двоих. В примере §10 она стоит отдельной
// строкой, а не делится пополам между людьми.
const CommonBucket = "Общее"

// PartnerBucket — трата «на партнёра», когда второй участник боту ещё не
// известен. Врать про «общее» здесь нельзя: это разные деньги.
const PartnerBucket = "Партнёру"

// Line — строка блока отчёта.
type Line struct {
	Name    string
	Amount  decimal.Decimal
	Percent int
}

// Month — готовый отчёт за календарный месяц.
type Month struct {
	Year          int
	Month         time.Month
	Total         decimal.Decimal
	Categories    []Line
	Payers        []Line
	Beneficiaries []Line
}

// MonthRange — границы месяца в нужной таймзоне. В базу уходят как UTC (§10).
func MonthRange(year int, month time.Month, loc *time.Location) (from, to time.Time) {
	from = time.Date(year, month, 1, 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 1, 0)
}

// DayRange — границы суток в нужной таймзоне.
func DayRange(day time.Time, loc *time.Location) (from, to time.Time) {
	d := day.In(loc)
	from = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 0, 1)
}

// BuildMonth собирает отчёт из строк расходов.
func BuildMonth(year int, month time.Month, rows []storage.ExpenseRow, users []storage.User) Month {
	m := Month{Year: year, Month: month, Total: decimal.Zero}

	byCategory := map[string]decimal.Decimal{}
	byPayer := map[int64]decimal.Decimal{}
	spentOn := map[int64]decimal.Decimal{}
	common := decimal.Zero
	unknownPartner := decimal.Zero

	for _, r := range rows {
		m.Total = m.Total.Add(r.Amount)

		name := r.CategoryName
		if name == "" {
			name = NoCategory
		}
		byCategory[name] = byCategory[name].Add(r.Amount)
		byPayer[r.PayerID] = byPayer[r.PayerID].Add(r.Amount)

		switch r.Beneficiary {
		case classify.BenPayer:
			spentOn[r.PayerID] = spentOn[r.PayerID].Add(r.Amount)
		case classify.BenPartner:
			if partner, ok := partnerOf(r.PayerID, users); ok {
				spentOn[partner] = spentOn[partner].Add(r.Amount)
			} else {
				// Второго участника бот ещё не видел: деньги из отчёта
				// пропасть не должны, но и общими они не стали.
				unknownPartner = unknownPartner.Add(r.Amount)
			}
		default:
			common = common.Add(r.Amount)
		}
	}

	m.Categories = sortedLines(namedSums(byCategory), m.Total)
	m.Payers = sortedLines(userSums(byPayer, users), decimal.Zero)

	beneficiaries := userSums(spentOn, users)
	if common.IsPositive() {
		beneficiaries = append(beneficiaries, Line{Name: CommonBucket, Amount: common})
	}
	if unknownPartner.IsPositive() {
		beneficiaries = append(beneficiaries, Line{Name: PartnerBucket, Amount: unknownPartner})
	}
	m.Beneficiaries = sortedLines(beneficiaries, decimal.Zero)
	return m
}

// partnerOf — второй участник бюджета. Бот рассчитан на двоих; если людей
// больше, «на партнёра» становится бессмысленным, и мы это признаём.
func partnerOf(payerID int64, users []storage.User) (int64, bool) {
	if len(users) != 2 {
		return 0, false
	}
	if users[0].ID == payerID {
		return users[1].ID, true
	}
	if users[1].ID == payerID {
		return users[0].ID, true
	}
	return 0, false
}

func namedSums(sums map[string]decimal.Decimal) []Line {
	out := make([]Line, 0, len(sums))
	for name, amount := range sums {
		out = append(out, Line{Name: name, Amount: amount})
	}
	return out
}

func userSums(sums map[int64]decimal.Decimal, users []storage.User) []Line {
	names := make(map[int64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Name
	}

	out := make([]Line, 0, len(sums))
	for id, amount := range sums {
		name := names[id]
		if name == "" {
			name = "Кто-то ещё"
		}
		out = append(out, Line{Name: name, Amount: amount})
	}
	return out
}

// sortedLines сортирует по убыванию суммы и считает доли от общего итога.
// Нулевой итог означает, что проценты в этом блоке не показываются (§10).
func sortedLines(lines []Line, total decimal.Decimal) []Line {
	sort.SliceStable(lines, func(i, j int) bool {
		if c := lines[i].Amount.Cmp(lines[j].Amount); c != 0 {
			return c > 0
		}
		return lines[i].Name < lines[j].Name
	})
	if total.IsZero() {
		return lines
	}
	hundred := decimal.NewFromInt(100)
	for i := range lines {
		lines[i].Percent = int(lines[i].Amount.Mul(hundred).Div(total).Round(0).IntPart())
	}
	return lines
}
