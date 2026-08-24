package app

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"budget/internal/classify"
	"budget/internal/report"
	"budget/internal/storage"
)

// txJSON — трата, какой её видит приложение.
//
// Суммы строками: в JSON число — это float64, а деньги через float гонять
// нельзя. UpdatedAt — версия записи, клиент возвращает её при правке.
type txJSON struct {
	ID          int64   `json:"id"`
	Payer       int64   `json:"payer"`
	Recipients  []int64 `json:"recipients"`
	Kind        string  `json:"kind"`
	Amount      string  `json:"amount"`
	Description string  `json:"description"`
	CategoryID  *int32  `json:"category_id"`
	RawText     string  `json:"raw_text"`
	SpentAt     string  `json:"spent_at"`
	UpdatedAt   string  `json:"updated_at,omitempty"`
	NeedsReview bool    `json:"needs_review,omitempty"`
	Pending     bool    `json:"pending,omitempty"`
}

type listResponse struct {
	Items []txJSON `json:"items"`
	Total int      `json:"total"`
}

func (s *Server) listTransactions(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		q := r.URL.Query()
		f := storage.TransactionFilter{
			PayerMemberID:     intQuery(q, "payer"),
			RecipientMemberID: recipientQuery(q),
			Category:          int32(intQuery(q, "category")),
			Kind:              q.Get("kind"),
			Pending:           q.Get("pending") == "1",
			Query:             q.Get("q"),
			Limit:             int(intQuery(q, "limit")),
			Offset:            int(intQuery(q, "offset")),
		}
		var err error
		if f.From, f.To, err = periodFrom(q, s.cfg.TZ); err != nil {
			fail(w, http.StatusBadRequest, "Не понял период.")
			return
		}

		items, total, err := c.group.ListTransactions(r.Context(), f)
		if err != nil {
			s.oops(w, "список трат", err)
			return
		}
		out := listResponse{Items: make([]txJSON, 0, len(items)), Total: total}
		for _, t := range items {
			out.Items = append(out.Items, toTx(t))
		}
		ok(w, out)
	})
}

func (s *Server) updateTransaction(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		id, okID := pathID(w, r)
		if !okID {
			return
		}

		var body struct {
			Amount      *string  `json:"amount"`
			Description *string  `json:"description"`
			CategoryID  *int32   `json:"category_id"`
			ClearCat    bool     `json:"clear_category"`
			Recipients  *[]int64 `json:"recipients"`
			Kind        *string  `json:"kind"`
			SpentAt     *string  `json:"spent_at"`
			UpdatedAt   *string  `json:"updated_at"`
		}
		if !decode(w, r, &body) {
			return
		}

		var patch storage.TransactionPatch
		if body.Amount != nil {
			amount, err := parseAmount(*body.Amount)
			if err != nil || !amount.IsPositive() {
				fail(w, http.StatusBadRequest, "Сумма должна быть больше нуля.")
				return
			}
			patch.Amount = &amount
		}
		if body.Description != nil {
			d := strings.TrimSpace(*body.Description)
			patch.Description = &d
		}
		switch {
		case body.ClearCat:
			var none *int32
			patch.CategoryID = &none
		case body.CategoryID != nil:
			cat := body.CategoryID
			patch.CategoryID = &cat
		}
		patch.Recipients = body.Recipients
		if body.Kind != nil {
			if !isKind(*body.Kind) {
				fail(w, http.StatusBadRequest, "Такого вида операции нет.")
				return
			}
			patch.Kind = body.Kind
		}
		if body.SpentAt != nil {
			at, err := time.Parse(time.RFC3339, *body.SpentAt)
			if err != nil {
				fail(w, http.StatusBadRequest, "Не понял дату.")
				return
			}
			patch.SpentAt = &at
		}

		var expected *time.Time
		if body.UpdatedAt != nil && *body.UpdatedAt != "" {
			at, err := time.Parse(time.RFC3339Nano, *body.UpdatedAt)
			if err != nil {
				fail(w, http.StatusBadRequest, "Не понял версию записи.")
				return
			}
			expected = &at
		}

		updated, err := c.group.UpdateTransaction(r.Context(), id, patch, expected)
		switch {
		case errors.Is(err, storage.ErrNoRows):
			http.NotFound(w, r)
			return
		case errors.Is(err, storage.ErrVersionConflict):
			// Не затираем чужую работу молча: одну трату правят с двух
			// телефонов чаще, чем кажется.
			fail(w, http.StatusConflict, "Запись успели изменить — открой её заново.")
			return
		case err != nil:
			s.oops(w, "правка траты", err)
			return
		}

		// Ручная правка учит словарь: в следующий раз то же слово разберётся
		// без сети. Правило одно на бота и на приложение — иначе поведение
		// быстрого пути зависело бы от того, где нажали кнопку.
		if patch.CategoryID != nil || patch.Recipients != nil {
			classify.RememberManual(r.Context(), c.group, updated, s.log)
		}
		ok(w, toTx(updated))
	})
}

func (s *Server) deleteTransaction(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		id, okID := pathID(w, r)
		if !okID {
			return
		}
		deleted, err := c.group.DeleteTransaction(r.Context(), id)
		if err != nil {
			s.oops(w, "удаление траты", err)
			return
		}
		if !deleted {
			http.NotFound(w, r)
			return
		}
		ok(w, nil)
	})
}

func (s *Server) restoreTransaction(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		id, okID := pathID(w, r)
		if !okID {
			return
		}
		restored, err := c.group.RestoreTransaction(r.Context(), id)
		if err != nil {
			s.oops(w, "возврат траты", err)
			return
		}
		if !restored {
			http.NotFound(w, r)
			return
		}
		tx, err := c.group.Transaction(r.Context(), id)
		if err != nil {
			s.oops(w, "чтение возвращённой траты", err)
			return
		}
		ok(w, toTx(tx))
	})
}

// --- отчёты ---

type lineJSON struct {
	ID      int64  `json:"id"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Amount  string `json:"amount"`
	Percent int    `json:"percent"`
}

type monthResponse struct {
	Year          int        `json:"year"`
	Month         int        `json:"month"`
	Total         string     `json:"total"`
	Categories    []lineJSON `json:"categories"`
	Payers        []lineJSON `json:"payers"`
	Beneficiaries []lineJSON `json:"beneficiaries"`
	// Compare — сопоставимый отрезок прошлого месяца. Nil, если сравнивать
	// нечестно: первые дни месяца дают разброс в сотни процентов.
	Compare *compareJSON `json:"compare"`
	// Review — сколько записей за месяц ждут человека.
	Review int `json:"review"`
}

type compareJSON struct {
	Previous string `json:"previous"`
	Current  string `json:"current"`
	Days     int    `json:"days"`
	Partial  bool   `json:"partial"`
}

func (s *Server) monthReport(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		year, month, err := monthFrom(r, s.cfg.TZ)
		if err != nil {
			fail(w, http.StatusBadRequest, "Не понял месяц.")
			return
		}
		from, to := report.MonthRange(year, month, s.cfg.TZ)

		rows, err := c.group.Expenses(r.Context(), from, to)
		if err != nil {
			s.oops(w, "расходы", err)
			return
		}
		members, err := c.group.AllMembers(r.Context())
		if err != nil {
			s.oops(w, "участники", err)
			return
		}
		m := report.BuildMonth(year, month, rows, members)

		out := monthResponse{
			Year: year, Month: int(month), Total: m.Total.String(),
			Categories:    toLines(m.Categories),
			Payers:        toLines(m.Payers),
			Beneficiaries: toLines(m.Beneficiaries),
		}
		if out.Review, err = c.group.PendingReview(r.Context(), from, to); err != nil {
			s.oops(w, "записи на проверку", err)
			return
		}
		if cmp, okCmp := report.ComparableRange(time.Now(), year, month, s.cfg.TZ); okCmp {
			prev, err := c.group.TotalExpenses(r.Context(), cmp.From, cmp.To)
			if err != nil {
				s.oops(w, "прошлый месяц", err)
				return
			}
			cur, err := c.group.TotalExpenses(r.Context(), cmp.CurrentFrom, cmp.CurrentTo)
			if err != nil {
				s.oops(w, "текущий отрезок", err)
				return
			}
			out.Compare = &compareJSON{
				Previous: prev.String(), Current: cur.String(),
				Days: cmp.Days, Partial: cmp.Partial,
			}
		}
		ok(w, out)
	})
}

// dayJSON — расход за день вместе с раскладкой по категориям.
//
// Раскладка едет тем же запросом, что и итог дня: график по дням без неё
// показывает, когда потратили, но не на что, а это половина вопроса.
type dayJSON struct {
	Day    string `json:"day"`
	Amount string `json:"amount"`
	// By — категория (нулевой ключ, если её нет) → сумма за этот день.
	By map[string]string `json:"by"`
}

func (s *Server) dayReport(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		year, month, err := monthFrom(r, s.cfg.TZ)
		if err != nil {
			fail(w, http.StatusBadRequest, "Не понял месяц.")
			return
		}
		from, to := report.MonthRange(year, month, s.cfg.TZ)

		parts, err := c.group.DailyByCategory(r.Context(), from, to, s.cfg.TZ.String())
		if err != nil {
			s.oops(w, "расходы по дням", err)
			return
		}

		// Порядок дней задаёт запрос; здесь только склейка, поэтому отдельный
		// список ключей вместо обхода map — иначе дни в ответе перемешаются.
		byDay := map[string]*dayJSON{}
		order := make([]string, 0, len(parts))
		for _, p := range parts {
			d, seen := byDay[p.Day]
			if !seen {
				d = &dayJSON{Day: p.Day, Amount: "0", By: map[string]string{}}
				byDay[p.Day] = d
				order = append(order, p.Day)
			}
			key := "0"
			if p.CategoryID != nil {
				key = strconv.Itoa(int(*p.CategoryID))
			}
			d.By[key] = p.Amount.String()

			total, _ := parseAmount(d.Amount)
			d.Amount = total.Add(p.Amount).String()
		}

		out := make([]dayJSON, 0, len(order))
		for _, day := range order {
			out = append(out, *byDay[day])
		}
		ok(w, out)
	})
}

// monthStrip — полоса месяцев: она же навигация, она же тренд.
func (s *Server) monthStrip(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		// Год назад плюс текущий месяц: тринадцать столбиков — это ровно
		// столько, сколько влезает в ширину телефона без прокрутки.
		now := time.Now().In(s.cfg.TZ)
		to := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, s.cfg.TZ).AddDate(0, 1, 0)
		from := to.AddDate(0, -13, 0)

		months, err := c.group.MonthlyExpenses(r.Context(), from, to, s.cfg.TZ.String())
		if err != nil {
			s.oops(w, "расходы по месяцам", err)
			return
		}
		// Пустые месяцы дорисовываются здесь: пропуск в полосе читается как
		// «данных нет», а не как «трат не было», и это разные вещи.
		have := map[string]string{}
		for _, m := range months {
			have[strconv.Itoa(m.Year)+"-"+strconv.Itoa(m.Month)] = m.Amount.String()
		}
		out := make([]map[string]any, 0, 13)
		for cur := from; cur.Before(to); cur = cur.AddDate(0, 1, 0) {
			key := strconv.Itoa(cur.Year()) + "-" + strconv.Itoa(int(cur.Month()))
			amount, seen := have[key]
			if !seen {
				amount = "0"
			}
			out = append(out, map[string]any{
				"year": cur.Year(), "month": int(cur.Month()), "amount": amount,
			})
		}
		ok(w, out)
	})
}

// --- разбор параметров ---

func periodFrom(q map[string][]string, loc *time.Location) (from, to time.Time, err error) {
	get := func(k string) string {
		if v, seen := q[k]; seen && len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if s := get("from"); s != "" {
		if from, err = time.ParseInLocation("2006-01-02", s, loc); err != nil {
			return
		}
	}
	if s := get("to"); s != "" {
		if to, err = time.ParseInLocation("2006-01-02", s, loc); err != nil {
			return
		}
		// Верхняя граница включительно: «по 31 июля» человек понимает так,
		// а не «до 31 июля 00:00».
		to = to.AddDate(0, 0, 1)
	}
	return from, to, nil
}

func monthFrom(r *http.Request, loc *time.Location) (int, time.Month, error) {
	q := r.URL.Query()
	now := time.Now().In(loc)

	year := int(intQuery(q, "year"))
	if year == 0 {
		year = now.Year()
	}
	month := int(intQuery(q, "month"))
	if month == 0 {
		month = int(now.Month())
	}
	if year < 2000 || year > 2999 || month < 1 || month > 12 {
		return 0, 0, errNotAMonth
	}
	return year, time.Month(month), nil
}

var errNotAMonth = errors.New("не месяц")

func intQuery(q map[string][]string, key string) int64 {
	v, seen := q[key]
	if !seen || len(v) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(v[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// recipientQuery различает «на всю группу» и «неважно».
func recipientQuery(q map[string][]string) int64 {
	v, seen := q["recipient"]
	if !seen || len(v) == 0 {
		return 0
	}
	if v[0] == "common" {
		return storage.CommonRecipient
	}
	n, _ := strconv.ParseInt(v[0], 10, 64)
	return n
}

func isKind(s string) bool {
	return s == storage.KindExpense || s == storage.KindIncome || s == storage.KindTransfer
}

func toTx(t storage.Transaction) txJSON {
	out := txJSON{
		ID: t.ID, Payer: t.PayerMemberID, Recipients: t.Recipients,
		Kind: t.Kind, Amount: t.Amount.String(), Description: t.Description,
		CategoryID: t.CategoryID, RawText: t.RawText,
		SpentAt:     t.SpentAt.UTC().Format(time.RFC3339),
		NeedsReview: t.NeedsReview, Pending: t.NeedsClassification,
	}
	if out.Recipients == nil {
		out.Recipients = []int64{}
	}
	if t.UpdatedAt != nil {
		out.UpdatedAt = t.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func toLines(lines []report.Line) []lineJSON {
	out := make([]lineJSON, 0, len(lines))
	for _, l := range lines {
		out = append(out, lineJSON{
			ID: l.ID, Key: l.Key, Name: l.Name,
			Amount: l.Amount.String(), Percent: l.Percent,
		})
	}
	return out
}
