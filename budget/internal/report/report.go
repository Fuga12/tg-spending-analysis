// Package report считает месячный отчёт: четыре блока из §10.
//
// SQL сознательно простой — из базы приезжают строки (плательщик, получатели,
// сумма), а раскладка «на кого ушло» считается здесь.
package report

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
)

// NoCategory — как называется отсутствующая категория в отчёте (§10).
const NoCategory = "Без категории"

// CommonBucket — доля трат на всю группу. Она стоит отдельной строкой, а не
// делится между участниками: «продукты домой» — это общие деньги, и разложить
// их по людям значит выдумать раскладку, которой никто не делал.
const CommonBucket = "Общее"

// UnknownMember — подпись строки, для которой участник не нашёлся. Такого
// быть не должно, но потерять деньги из отчёта хуже, чем показать их без имени.
const UnknownMember = "Кто-то ещё"

// Line — строка блока отчёта.
type Line struct {
	// ID — идентификатор участника для строк «кто платил» и «на кого ушло».
	// Ноль у категорий и корзины «Общее». Фронт по нему назначает цвет:
	// смотрящий всегда первый слот (webapp-design.md §3.6).
	ID      int64
	Key     string
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

// BuildMonth собирает отчёт из строк расходов группы.
//
// members — участники вместе с ушедшими: траты человека, покинувшего группу,
// из истории никуда не делись, и подписать их надо.
func BuildMonth(year int, month time.Month, rows []storage.ExpenseRow, members []storage.Member) Month {
	m := Month{Year: year, Month: month, Total: decimal.Zero}

	byCategory := map[string]decimal.Decimal{}
	categoryIDs := map[string]int32{}
	byPayer := map[int64]decimal.Decimal{}
	spentOn := map[int64]decimal.Decimal{}
	common := decimal.Zero

	for _, r := range rows {
		m.Total = m.Total.Add(r.Amount)

		name := r.CategoryName
		if name == "" {
			name = NoCategory
		}
		byCategory[name] = byCategory[name].Add(r.Amount)
		if r.CategoryID != nil {
			categoryIDs[name] = *r.CategoryID
		}
		byPayer[r.PayerMemberID] = byPayer[r.PayerMemberID].Add(r.Amount)

		// Пустой список получателей означает «на всю группу» — такие деньги
		// стоят отдельной строкой и по людям не раскладываются.
		if len(r.Recipients) == 0 {
			common = common.Add(r.Amount)
			continue
		}
		for i, share := range splitEqually(r.Amount, len(r.Recipients)) {
			id := r.Recipients[i]
			spentOn[id] = spentOn[id].Add(share)
		}
	}

	m.Categories = sortedLines(namedSums(byCategory, categoryIDs), m.Total)
	m.Payers = sortedLines(memberSums(byPayer, members), decimal.Zero)

	beneficiaries := memberSums(spentOn, members)
	if common.IsPositive() {
		beneficiaries = append(beneficiaries, Line{Key: "common", Name: CommonBucket, Amount: common})
	}
	m.Beneficiaries = sortedLines(beneficiaries, decimal.Zero)
	return m
}

// splitEqually делит трату между несколькими получателями поровну.
//
// Остаток от деления достаётся первому: 1000 на троих — это 333,34 и два раза
// по 333,33, а не три раза по 333,33. Сумма долей обязана совпадать с суммой
// траты, иначе блок «на кого ушло» перестаёт сходиться с итогом, и в отчёте
// появляются копейки из ниоткуда.
func splitEqually(amount decimal.Decimal, n int) []decimal.Decimal {
	if n <= 1 {
		return []decimal.Decimal{amount}
	}
	out := make([]decimal.Decimal, n)
	share := amount.Div(decimal.NewFromInt(int64(n))).RoundDown(2)
	rest := amount
	for i := n - 1; i > 0; i-- {
		out[i] = share
		rest = rest.Sub(share)
	}
	out[0] = rest
	return out
}

func namedSums(sums map[string]decimal.Decimal, ids map[string]int32) []Line {
	out := make([]Line, 0, len(sums))
	for name, amount := range sums {
		// ID нужен фронту: тап по строке категории фильтрует список, а имя
		// в адресе ломается при первом же переименовании.
		out = append(out, Line{ID: int64(ids[name]), Name: name, Amount: amount})
	}
	return out
}

// memberSums подписывает суммы именами участников. Ключ — member_id, а не
// telegram id: один и тот же человек может побывать в группе дважды, и его
// траты за разные заходы — разные строки истории.
func memberSums(sums map[int64]decimal.Decimal, members []storage.Member) []Line {
	names := make(map[int64]string, len(members))
	for _, m := range members {
		names[m.ID] = m.Name
	}

	out := make([]Line, 0, len(sums))
	for id, amount := range sums {
		name := names[id]
		if name == "" {
			name = UnknownMember
		}
		out = append(out, Line{ID: id, Key: fmt.Sprintf("member:%d", id), Name: name, Amount: amount})
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

// ComparableRange — отрезок прошлого месяца, с которым честно сравнивать
// текущий (webapp-design.md §3.3).
//
// Правила: незакрытый месяц сравнивается с тем же числом дней прошлого, но
// не больше, чем в прошлом месяце вообще есть — иначе 31 июля сравнивалось бы
// с несуществующим 31 июня. Первые дни месяца не сравниваем совсем: два дня
// против двух дней дают разброс в сотни процентов, и это не информация.
func ComparableRange(now time.Time, year int, month time.Month, loc *time.Location) (Comparison, bool) {
	const minDays = 4

	start, end := MonthRange(year, month, loc)
	prevStart := start.AddDate(0, -1, 0)
	prevEnd := start

	current := now.In(loc)
	if current.Before(start) {
		// Месяц ещё не начался — сравнивать нечего.
		return Comparison{}, false
	}

	if current.Before(end) {
		// Месяц идёт: берём столько же дней, сколько прошло, но не больше,
		// чем в прошлом месяце вообще есть. Обрезать надо обе стороны, иначе
		// 31 июля сравнивается с 30 днями июня и дельта завышена.
		elapsed := int(startOfDay(current).Sub(start).Hours()/24) + 1
		if inPrev := int(prevEnd.Sub(prevStart).Hours() / 24); elapsed > inPrev {
			elapsed = inPrev
		}
		if elapsed < minDays {
			return Comparison{}, false
		}
		return Comparison{
			From: prevStart, To: prevStart.AddDate(0, 0, elapsed),
			CurrentFrom: start, CurrentTo: start.AddDate(0, 0, elapsed),
			Days: elapsed, Partial: true,
		}, true
	}

	// Месяц закрыт — сравниваем целиком с целым.
	return Comparison{
		From: prevStart, To: prevEnd,
		CurrentFrom: start, CurrentTo: end,
		Days: int(prevEnd.Sub(prevStart).Hours() / 24),
	}, true
}

// Comparison — что с чем сравнивать. Обе стороны заданы явно: сравнивать
// полный текущий месяц с обрезанным прошлым — значит завышать дельту.
type Comparison struct {
	From, To               time.Time // отрезок прошлого месяца
	CurrentFrom, CurrentTo time.Time // сопоставимый отрезок текущего
	Days                   int
	Partial                bool
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
