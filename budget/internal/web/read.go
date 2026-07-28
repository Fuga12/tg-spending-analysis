package web

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/report"
	"budget/internal/storage"
)

// txView — операция в том виде, в каком её ждёт фронт.
//
// Суммы уходят строками: в JSON число — это float64, а деньги через float
// гонять нельзя. День считает сервер в таймзоне бота: если группировать в
// браузере, трата, записанная в 00:30 по Москве, у телефона в другой зоне
// уедет во вчера (webapp-design.md §3.9).
type txView struct {
	ID          int64   `json:"id"`
	Day         string  `json:"day"`
	Amount      string  `json:"amount"`
	Description string  `json:"description"`
	CategoryID  *int32  `json:"category_id"`
	Category    string  `json:"category"`
	PayerID     int64   `json:"payer_id"`
	Beneficiary string  `json:"beneficiary"`
	Kind        string  `json:"kind"`
	SpentAt     string  `json:"spent_at"`
	RawText     string  `json:"raw_text"`
	NeedsReview bool    `json:"needs_review"`
	UpdatedAt   *string `json:"updated_at"`
	Mine        bool    `json:"mine"`
}

type listResponse struct {
	Items   []txView `json:"items"`
	Total   int      `json:"total"`
	HasMore bool     `json:"has_more"`
}

func (s *Server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := storage.TransactionFilter{
		PayerID:  intParam(q.Get("payer")),
		Category: categoryParam(q.Get("category")),
		Kind:     q.Get("kind"),
		Pending:  q.Get("pending") == "1",
		Query:    q.Get("q"),
		Limit:    int(intParam(q.Get("limit"))),
		Offset:   int(max64(intParam(q.Get("offset")), 0)),
	}

	// Поиск идёт по всем месяцам — иначе искать незачем (§3.2).
	if filter.Query == "" {
		year, month, ok := monthParam(q, s.cfg.TZ)
		if !ok {
			writeError(w, http.StatusBadRequest, "не понял месяц")
			return
		}
		filter.From, filter.To = report.MonthRange(year, month, s.cfg.TZ)
	}

	items, total, err := s.store.ListTransactions(r.Context(), filter)
	if err != nil {
		s.log.Error("список операций", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}

	me := userID(r)
	out := make([]txView, 0, len(items))
	for _, t := range items {
		out = append(out, s.view(t, me))
	}
	writeJSON(w, http.StatusOK, listResponse{
		Items:   out,
		Total:   total,
		HasMore: filter.Offset+len(items) < total,
	})
}

func (s *Server) view(t storage.Transaction, me int64) txView {
	v := txView{
		ID:          t.ID,
		Day:         t.SpentAt.In(s.cfg.TZ).Format("2006-01-02"),
		Amount:      t.Amount.String(),
		Description: t.Description,
		CategoryID:  t.CategoryID,
		Category:    t.CategoryName,
		PayerID:     t.PayerID,
		Beneficiary: t.Beneficiary,
		Kind:        t.Kind,
		SpentAt:     t.SpentAt.In(s.cfg.TZ).Format(time.RFC3339),
		RawText:     t.RawText,
		NeedsReview: t.NeedsReview,
		Mine:        t.PayerID == me,
	}
	if t.UpdatedAt != nil {
		updated := t.UpdatedAt.In(s.cfg.TZ).Format(time.RFC3339)
		v.UpdatedAt = &updated
	}
	return v
}

// lineView — строка блока отчёта.
type lineView struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Amount  string `json:"amount"`
	Percent int    `json:"percent"`
}

// compareView — сопоставимый отрезок прошлого месяца.
//
// Неполный месяц сравнивается с тем же числом дней прошлого, иначе третьего
// числа выходит «−90%», и это враньё. При совсем коротком отрезке дельту не
// показываем вовсе (webapp-design.md §3.3).
type compareView struct {
	Days       int    `json:"days"`
	Previous   string `json:"previous"`
	Percent    int    `json:"percent"`
	HasPercent bool   `json:"has_percent"`
	Difference string `json:"difference"`
	Partial    bool   `json:"partial"`
}

type monthResponse struct {
	Year          int          `json:"year"`
	Month         int          `json:"month"`
	Total         string       `json:"total"`
	Compare       *compareView `json:"compare"`
	Categories    []lineView   `json:"categories"`
	Payers        []lineView   `json:"payers"`
	Beneficiaries []lineView   `json:"beneficiaries"`
	Pending       int          `json:"pending"`
}

func (s *Server) handleMonth(w http.ResponseWriter, r *http.Request) {
	year, month, ok := monthParam(r.URL.Query(), s.cfg.TZ)
	if !ok {
		writeError(w, http.StatusBadRequest, "не понял месяц")
		return
	}

	from, to := report.MonthRange(year, month, s.cfg.TZ)
	rows, err := s.store.Expenses(r.Context(), from, to)
	if err != nil {
		s.log.Error("отчёт за месяц", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.log.Error("список пользователей", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	pending, err := s.store.PendingReview(r.Context(), from, to)
	if err != nil {
		s.log.Warn("счётчик записей на проверку", "err", err)
	}

	m := report.BuildMonth(year, month, rows, users)
	resp := monthResponse{
		Year:          year,
		Month:         int(month),
		Total:         m.Total.String(),
		Categories:    lines(m.Categories),
		Payers:        lines(m.Payers),
		Beneficiaries: lines(m.Beneficiaries),
		Pending:       pending,
	}
	if cmp, ok := s.compare(r, year, month, m.Total); ok {
		resp.Compare = cmp
	}
	writeJSON(w, http.StatusOK, resp)
}

// minCompareBase — ниже этой суммы проценты бессмысленны: 350 ₽ против
// 25 000 дают «+7043%», и это не информация, а шум (webapp-design.md §3.3).
var minCompareBase = decimal.NewFromInt(1000)

// maxComparePercent — за этой границей показываем разницу в рублях.
const maxComparePercent = 200

// compare считает сопоставимый отрезок прошлого месяца.
func (s *Server) compare(r *http.Request, year int, month time.Month, total decimal.Decimal) (*compareView, bool) {
	if !total.IsPositive() {
		// Пустой месяц сравнивать не с чем.
		return nil, false
	}
	c, ok := report.ComparableRange(s.now(), year, month, s.cfg.TZ)
	if !ok {
		return nil, false
	}

	prev, err := s.sum(r, c.From, c.To)
	if err != nil {
		s.log.Warn("сравнение с прошлым месяцем", "err", err)
		return nil, false
	}
	if !prev.IsPositive() {
		return nil, false
	}

	// Сравниваем сопоставимые отрезки: у незакрытого месяца это не весь
	// месяц, а столько же дней, сколько прошло.
	current := total
	if c.Partial {
		if current, err = s.sum(r, c.CurrentFrom, c.CurrentTo); err != nil {
			s.log.Warn("сопоставимый отрезок текущего месяца", "err", err)
			return nil, false
		}
	}

	view := &compareView{Days: c.Days, Previous: prev.String(), Partial: c.Partial}
	percent := current.Sub(prev).Mul(decimal.NewFromInt(100)).Div(prev).Round(0).IntPart()
	if prev.LessThan(minCompareBase) || percent > maxComparePercent || percent < -maxComparePercent {
		// База слишком мала — процент врёт. Показываем разницу в рублях.
		view.Difference = current.Sub(prev).String()
	} else {
		view.Percent = int(percent)
		view.HasPercent = true
	}
	return view, true
}

// sum складывает расходы за отрезок.
func (s *Server) sum(r *http.Request, from, to time.Time) (decimal.Decimal, error) {
	rows, err := s.store.Expenses(r.Context(), from, to)
	if err != nil {
		return decimal.Zero, err
	}
	out := decimal.Zero
	for _, row := range rows {
		out = out.Add(row.Amount)
	}
	return out, nil
}

func lines(in []report.Line) []lineView {
	out := make([]lineView, 0, len(in))
	for _, l := range in {
		out = append(out, lineView{ID: l.ID, Name: l.Name, Amount: l.Amount.String(), Percent: l.Percent})
	}
	return out
}

type categoryView struct {
	ID          int32  `json:"id"`
	Name        string `json:"name"`
	Beneficiary string `json:"beneficiary"`
}

func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.store.Categories(r.Context())
	if err != nil {
		s.log.Error("категории", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	out := make([]categoryView, 0, len(cats))
	for _, c := range cats {
		out = append(out, categoryView{ID: c.ID, Name: c.Name, Beneficiary: c.DefaultBeneficiary})
	}
	writeJSON(w, http.StatusOK, out)
}

// monthParam разбирает year и month; без них — текущий месяц.
func monthParam(q map[string][]string, loc *time.Location) (int, time.Month, bool) {
	now := time.Now().In(loc)
	year, month := now.Year(), now.Month()

	if raw := first(q["year"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2000 || n > 2200 {
			return 0, 0, false
		}
		year = n
	}
	if raw := first(q["month"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 12 {
			return 0, 0, false
		}
		month = time.Month(n)
	}
	return year, month, true
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return strings.TrimSpace(v[0])
}

// categoryParam не даёт большому числу молча обрезаться до чужой категории.
func categoryParam(raw string) int32 {
	n := intParam(raw)
	if n <= 0 || n > math.MaxInt32 {
		return 0
	}
	return int32(n)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func intParam(raw string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
