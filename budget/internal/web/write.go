package web

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

// Границы правок (webapp.md §4). Сумма — единственное поле, которое веб
// позволяет менять, а бот нет, поэтому проверяется здесь, а не в базе.
var (
	maxAmount   = decimal.RequireFromString("9999999999")
	minSpentAt  = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	maxFuture   = 24 * time.Hour
	maxDescRune = 64
)

// txPatch — тело PATCH. Указатели, чтобы отличать «не трогать» от «обнулить».
type txPatch struct {
	Amount      *string `json:"amount"`
	Description *string `json:"description"`
	CategoryID  *int32  `json:"category_id"`
	ClearCat    bool    `json:"clear_category"`
	Beneficiary *string `json:"beneficiary"`
	Kind        *string `json:"kind"`
	SpentAt     *string `json:"spent_at"`
	Deleted     *bool   `json:"deleted"`

	// UpdatedAt — версия, которую видел клиент. Без неё правка человека
	// молча затиралась бы догадкой воркера (webapp.md §4).
	UpdatedAt *string `json:"updated_at"`
}

func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "не понял, какая это запись")
		return
	}

	var patch txPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "не разобрал запрос")
		return
	}

	cats, err := s.store.Categories(r.Context())
	if err != nil {
		s.log.Error("категории", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}

	p, err := s.buildPatch(patch, cats)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Возврат удалённой — отдельная операция: версии у клиента нет и быть не
	// может, а требовать её значит сделать «Вернуть» неработающей кнопкой.
	if p.Restore && onlyRestore(patch) {
		tx, err := s.store.RestoreTransaction(r.Context(), id)
		if err != nil {
			s.writeUpdateError(w, r, id, err)
			return
		}
		writeJSON(w, http.StatusOK, s.view(tx, userID(r)))
		return
	}

	version, err := parseVersion(patch.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "не понял версию записи")
		return
	}

	// Состояние до правки решает, можно ли учить словарь: у записи,
	// разобранной вслепую, описание — это весь текст сообщения.
	before, err := s.store.Transaction(r.Context(), id)
	learnable := err == nil && !before.NeedsClassification && !before.NeedsReview

	tx, err := s.store.UpdateTransaction(r.Context(), id, p, version)
	if err != nil {
		s.writeUpdateError(w, r, id, err)
		return
	}

	// Словарь учится только на смене категории или бенефициара — как кнопки
	// бота. Правка одной суммы прибивать слова к категории не должна.
	if learnable && (p.CategoryID != nil || p.Beneficiary != nil) {
		classify.RememberManual(r.Context(), s.store, tx, s.log)
	}
	writeJSON(w, http.StatusOK, s.view(tx, userID(r)))
}

// onlyRestore — в теле нет ничего, кроме «верни удалённое».
func onlyRestore(p txPatch) bool {
	return p.Amount == nil && p.Description == nil && p.CategoryID == nil && !p.ClearCat &&
		p.Beneficiary == nil && p.Kind == nil && p.SpentAt == nil
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "не понял, какая это запись")
		return
	}

	deleted, err := s.store.DeleteTransaction(r.Context(), id)
	if err != nil {
		s.log.Error("удаление", "err", err, "tx", id)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	if !deleted {
		s.writeUpdateError(w, r, id, storage.ErrNoRows)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// newTx — тело POST: ручное добавление записи задним числом.
type newTx struct {
	Amount      string `json:"amount"`
	Description string `json:"description"`
	CategoryID  *int32 `json:"category_id"`
	Beneficiary string `json:"beneficiary"`
	Kind        string `json:"kind"`
	SpentAt     string `json:"spent_at"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body newTx
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "не разобрал запрос")
		return
	}

	cats, err := s.store.Categories(r.Context())
	if err != nil {
		s.log.Error("категории", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}

	amount, err := parseAmount(body.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	spentAt := s.now()
	if body.SpentAt != "" {
		var err error
		if spentAt, err = s.parseSpentAt(body.SpentAt); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	kind := body.Kind
	if kind == "" {
		kind = classify.KindExpense
	}
	if !isKind(kind) {
		writeError(w, http.StatusBadRequest, "не понял тип операции")
		return
	}
	beneficiary := body.Beneficiary
	if beneficiary == "" {
		beneficiary = classify.BenPayer
	}
	if !isBeneficiary(beneficiary) {
		writeError(w, http.StatusBadRequest, "не понял, на кого потрачено")
		return
	}
	category := body.CategoryID
	if category != nil && !hasCategory(cats, *category) {
		writeError(w, http.StatusBadRequest, "нет такой категории")
		return
	}
	// Перевод — не трата на категорию (plan.md §3), правило одно на оба
	// интерфейса.
	if kind == classify.KindTransfer {
		beneficiary, category = classify.BenPartner, nil
	}

	description := trimDescription(body.Description)
	if description == "" {
		description = "без описания"
	}

	tx := storage.Transaction{
		PayerID:     userID(r),
		Beneficiary: beneficiary,
		Kind:        kind,
		Amount:      amount,
		Description: description,
		CategoryID:  category,
		RawText:     "",
		SpentAt:     spentAt,
	}
	id, err := s.store.InsertTransaction(r.Context(), tx)
	if err != nil {
		s.log.Error("добавление записи", "err", err)
		writeError(w, http.StatusInternalServerError, "не смог записать")
		return
	}
	tx.ID = id
	tx.CategoryName = categoryName(cats, category)

	classify.RememberManual(r.Context(), s.store, tx, s.log)
	writeJSON(w, http.StatusCreated, s.view(tx, userID(r)))
}

// buildPatch проверяет присланное и превращает в патч для хранилища.
func (s *Server) buildPatch(in txPatch, cats []storage.Category) (storage.TransactionPatch, error) {
	var out storage.TransactionPatch

	if in.Amount != nil {
		amount, err := parseAmount(*in.Amount)
		if err != nil {
			return out, err
		}
		out.Amount = &amount
	}
	if in.Description != nil {
		desc := trimDescription(*in.Description)
		if desc == "" {
			return out, errors.New("описание не может быть пустым")
		}
		out.Description = &desc
	}
	if in.Beneficiary != nil {
		if !isBeneficiary(*in.Beneficiary) {
			return out, errors.New("не понял, на кого потрачено")
		}
		out.Beneficiary = in.Beneficiary
	}
	if in.Kind != nil {
		if !isKind(*in.Kind) {
			return out, errors.New("не понял тип операции")
		}
		out.Kind = in.Kind
	}
	if in.SpentAt != nil {
		spentAt, err := s.parseSpentAt(*in.SpentAt)
		if err != nil {
			return out, err
		}
		out.SpentAt = &spentAt
	}
	if in.CategoryID != nil {
		if !hasCategory(cats, *in.CategoryID) {
			return out, errors.New("нет такой категории")
		}
		cat := in.CategoryID
		out.CategoryID = &cat
	} else if in.ClearCat {
		var none *int32
		out.CategoryID = &none
	}
	if in.Deleted != nil && !*in.Deleted {
		out.Restore = true
	}

	// Перевод не бывает ни с категорией, ни с бенефициаром «на двоих».
	if out.Kind != nil && *out.Kind == classify.KindTransfer {
		partner := classify.BenPartner
		out.Beneficiary = &partner
		var none *int32
		out.CategoryID = &none
	}
	return out, nil
}

func (s *Server) writeUpdateError(w http.ResponseWriter, r *http.Request, id int64, err error) {
	switch {
	case errors.Is(err, storage.ErrVersionConflict):
		// Отдаём текущее состояние: интерфейс покажет, что изменилось,
		// и даст выбрать (webapp-design.md §4.1).
		if tx, readErr := s.store.Transaction(r.Context(), id); readErr == nil {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "запись изменилась",
				"current": s.view(tx, userID(r)),
			})
			return
		}
		writeError(w, http.StatusConflict, "запись изменилась")
	case errors.Is(err, storage.ErrNoRows):
		writeError(w, http.StatusNotFound, "этой записи больше нет")
	default:
		s.log.Error("правка записи", "err", err, "tx", id)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
	}
}

// amountLike — то, что вообще может быть суммой. Проверяется до разбора:
// «1e2000000000» decimal разворачивает в гигабайты и кладёт процесс вместе
// с ботом, причём разрыв соединения его уже не остановит.
var amountLike = regexp.MustCompile(`^\d{1,10}([.,]\d{1,2})?$`)

func parseAmount(raw string) (decimal.Decimal, error) {
	raw = strings.TrimSpace(raw)
	if !amountLike.MatchString(raw) {
		return decimal.Zero, errors.New("сумма должна быть числом вроде 1200 или 1200,50")
	}
	amount, err := decimal.NewFromString(strings.ReplaceAll(raw, ",", "."))
	if err != nil {
		return decimal.Zero, errors.New("сумма должна быть числом")
	}
	// Округление до копеек делается раньше проверки: «0.004» иначе прошло бы
	// её и упало на check (amount > 0) пятисоткой.
	amount = amount.Round(2)
	if !amount.IsPositive() {
		return decimal.Zero, errors.New("сумма должна быть больше нуля")
	}
	if amount.GreaterThan(maxAmount) {
		return decimal.Zero, errors.New("такая сумма не влезет")
	}
	return amount, nil
}

// parseSpentAt принимает и YYYY-MM-DD, и полную дату. День без времени
// считается полднем в таймзоне бота: иначе на границе суток запись уезжает
// на день назад.
func (s *Server) parseSpentAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("не понял дату")
	}

	spentAt, err := time.ParseInLocation("2006-01-02", raw, s.cfg.TZ)
	if err == nil {
		spentAt = spentAt.Add(12 * time.Hour)
	} else if spentAt, err = time.Parse(time.RFC3339, raw); err != nil {
		return time.Time{}, errors.New("не понял дату")
	}

	if spentAt.Before(minSpentAt) {
		return time.Time{}, errors.New("дата слишком старая")
	}
	if spentAt.After(s.now().Add(maxFuture)) {
		return time.Time{}, errors.New("дата в будущем")
	}
	return spentAt, nil
}

func parseVersion(raw *string) (*time.Time, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	v, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func trimDescription(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > maxDescRune {
		r = r[:maxDescRune]
	}
	return strings.TrimSpace(string(r))
}

func isBeneficiary(s string) bool {
	return s == classify.BenPayer || s == classify.BenPartner || s == classify.BenBoth
}

func isKind(s string) bool {
	return s == classify.KindExpense || s == classify.KindIncome || s == classify.KindTransfer
}

func hasCategory(cats []storage.Category, id int32) bool {
	for _, c := range cats {
		if c.ID == id {
			return true
		}
	}
	return false
}

func categoryName(cats []storage.Category, id *int32) string {
	if id == nil {
		return ""
	}
	for _, c := range cats {
		if c.ID == *id {
			return c.Name
		}
	}
	return ""
}

// pathID достаёт идентификатор из /api/transactions/{id}.
func pathID(path string) (int64, bool) {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(path[idx+1:], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// categoryPatch — имя и подсказка. Подсказка уходит в JSON-схему запроса и
// прямо влияет на то, как модель раскладывает траты (plan.md §6).
type categoryPatch struct {
	Name        string `json:"name"`
	Hint        string `json:"hint"`
	Beneficiary string `json:"beneficiary"`
}

// Границы: имя в enum схемы, подсказка в описание поля. Длинные строки
// раздувают промпт, а он уходит на каждый разбор.
const (
	maxCategoryName = 32
	maxCategoryHint = 120
	maxCategories   = 30
)

func (s *Server) handleCategoryPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path)
	if !ok || id > math.MaxInt32 {
		writeError(w, http.StatusBadRequest, "не понял, какая это категория")
		return
	}

	var body categoryPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "не разобрал запрос")
		return
	}

	name := trimTo(strings.TrimSpace(body.Name), maxCategoryName)
	hint := trimTo(strings.TrimSpace(body.Hint), maxCategoryHint)
	if name == "" {
		writeError(w, http.StatusBadRequest, "у категории должно быть название")
		return
	}

	cats, err := s.store.Categories(r.Context())
	if err != nil {
		s.log.Error("категории", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	// Имена уходят в enum схемы, и одинаковых там быть не может.
	for _, c := range cats {
		if c.ID != int32(id) && strings.EqualFold(c.Name, name) {
			writeError(w, http.StatusBadRequest, "категория с таким названием уже есть")
			return
		}
	}

	beneficiary := body.Beneficiary
	if !isBeneficiary(beneficiary) {
		// Не прислали — оставляем как было.
		for _, c := range cats {
			if c.ID == int32(id) {
				beneficiary = c.DefaultBeneficiary
			}
		}
	}

	updated, err := s.store.UpdateCategory(r.Context(), int32(id), name, hint, beneficiary)
	if err != nil {
		s.log.Error("правка категории", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "нет такой категории")
		return
	}
	s.log.Info("категория изменена", "id", id, "name", name, "по умолчанию", beneficiary)
	writeJSON(w, http.StatusOK, categoryView{
		ID: int32(id), Name: name, Hint: hint, Beneficiary: beneficiary,
	})
}

// trimTo обрезает строку до n символов, не разрывая руны.
func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// handleCategoryCreate заводит новую категорию.
func (s *Server) handleCategoryCreate(w http.ResponseWriter, r *http.Request) {
	var body categoryPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "не разобрал запрос")
		return
	}

	name := trimTo(strings.TrimSpace(body.Name), maxCategoryName)
	hint := trimTo(strings.TrimSpace(body.Hint), maxCategoryHint)
	if name == "" {
		writeError(w, http.StatusBadRequest, "у категории должно быть название")
		return
	}

	cats, err := s.store.Categories(r.Context())
	if err != nil {
		s.log.Error("категории", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	for _, c := range cats {
		if strings.EqualFold(c.Name, name) {
			writeError(w, http.StatusBadRequest, "категория с таким названием уже есть")
			return
		}
	}
	// Каждая категория удлиняет промпт и усложняет выбор модели, поэтому
	// какой-то потолок нужен — но щедрый.
	if len(cats) >= maxCategories {
		writeError(w, http.StatusBadRequest, "категорий уже слишком много, дальше модель начнёт путаться")
		return
	}

	beneficiary := body.Beneficiary
	if !isBeneficiary(beneficiary) {
		beneficiary = classify.BenBoth
	}

	created, err := s.store.CreateCategory(r.Context(), name, hint, beneficiary)
	if err != nil {
		s.log.Error("создание категории", "err", err)
		writeError(w, http.StatusInternalServerError, "не смог создать")
		return
	}
	s.log.Info("категория создана", "id", created.ID, "name", name)
	writeJSON(w, http.StatusCreated, categoryView{
		ID: created.ID, Name: created.Name, Hint: created.Hint,
		Beneficiary: created.DefaultBeneficiary,
	})
}
