package web

import (
	"encoding/json"
	"errors"
	"net/http"
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

	version, err := parseVersion(patch.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "не понял версию записи")
		return
	}

	tx, err := s.store.UpdateTransaction(r.Context(), id, userID(r), p, version)
	if err != nil {
		s.writeUpdateError(w, r, id, err)
		return
	}

	// Ручная правка учит словарь — тем же правилом, что и кнопки бота.
	classify.RememberManual(r.Context(), s.store, tx, s.log)
	writeJSON(w, http.StatusOK, s.view(tx, userID(r)))
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "не понял, какая это запись")
		return
	}

	deleted, err := s.store.DeleteTransaction(r.Context(), id, userID(r))
	if err != nil {
		s.log.Error("удаление", "err", err, "tx", id)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	if !deleted {
		// Либо чужая, либо уже удалена — различаем чтением.
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
	spentAt, err := s.parseSpentAt(body.SpentAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kind := body.Kind
	if !isKind(kind) {
		kind = classify.KindExpense
	}
	beneficiary := body.Beneficiary
	if !isBeneficiary(beneficiary) {
		beneficiary = classify.BenPayer
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
		RawText:     "добавлено на сайте",
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
	case errors.Is(err, storage.ErrNotOwner):
		writeError(w, http.StatusForbidden, "это запись партнёра")
	case errors.Is(err, storage.ErrNoRows):
		writeError(w, http.StatusNotFound, "этой записи больше нет")
	default:
		s.log.Error("правка записи", "err", err, "tx", id)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
	}
}

func parseAmount(raw string) (decimal.Decimal, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(strings.ReplaceAll(raw, ",", ".")))
	if err != nil {
		return decimal.Zero, errors.New("сумма должна быть числом")
	}
	if !amount.IsPositive() {
		return decimal.Zero, errors.New("сумма должна быть больше нуля")
	}
	if amount.GreaterThan(maxAmount) {
		return decimal.Zero, errors.New("такая сумма не влезет")
	}
	return amount.Round(2), nil
}

// parseSpentAt принимает и YYYY-MM-DD, и полную дату. День без времени
// считается полднем в таймзоне бота: иначе на границе суток запись уезжает
// на день назад.
func (s *Server) parseSpentAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now(), nil
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
	v, err := time.Parse(time.RFC3339, *raw)
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
