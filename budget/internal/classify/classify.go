// Package classify превращает сообщение пользователя в набор транзакций.
//
// Порядок один и тот же всегда: сначала кэш-резолвер (§5), он отвечает
// мгновенно и без сети; если не сработал — LLM (§6); что бы ни вернула
// модель, результат проходит детерминированную валидацию (§8).
package classify

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
	"budget/internal/tokens"
)

// Бенефициар — на кого потрачено относительно плательщика (§3).
const (
	BenPayer   = "payer"
	BenPartner = "partner"
	BenBoth    = "both"
)

// Тип операции.
const (
	KindExpense  = "expense"
	KindIncome   = "income"
	KindTransfer = "transfer"
)

// CategoryOther — куда падает всё, что не разобрали (§8).
const CategoryOther = "Прочее"

// Item — одна разобранная трата, готовая к записи в transactions.
type Item struct {
	Amount      decimal.Decimal
	Description string
	CategoryID  *int32 // nil — без категории
	Beneficiary string
	Kind        string
	DaysAgo     int

	// Words — значимые слова описания, их запоминает кэш после успешного
	// разбора (§8). Для деградированного результата пусто.
	Words []string
}

// Source — каким путём получен результат. Нужен для метрики скорости (§5)
// и для команды /лимит (§7).
type Source string

const (
	SourceCache    Source = "cache"
	SourceLLM      Source = "llm"
	SourceDegraded Source = "degraded"
)

// Result — итог разбора одного сообщения.
type Result struct {
	Items  []Item
	Source Source
}

// ErrNoAmount — в сообщении нет ни одного числа, сохранять нечего (§8).
var ErrNoAmount = errors.New("в сообщении нет суммы")

// Dict — то, что классификатору нужно от хранилища.
type Dict interface {
	Categories(ctx context.Context) ([]storage.Category, error)
	LookupWords(ctx context.Context, userID int64, words []string) (map[string]storage.WordHit, error)
}

// LLM — обращение к модели. Возвращает разобранные моделью элементы как есть,
// без валидации: проверять их — дело validate.go.
type LLM interface {
	Parse(ctx context.Context, text string, cats []storage.Category) ([]RawItem, error)
}

// Service связывает кэш, модель и валидацию.
type Service struct {
	dict Dict
	llm  LLM
	log  *slog.Logger
}

func NewService(dict Dict, llm LLM, log *slog.Logger) *Service {
	return &Service{dict: dict, llm: llm, log: log}
}

// Classify разбирает сообщение пользователя.
func (s *Service) Classify(ctx context.Context, userID int64, text string) (*Result, error) {
	amounts := tokens.Extract(text)
	if len(amounts) == 0 {
		return nil, ErrNoAmount
	}

	cats, err := s.dict.Categories(ctx)
	if err != nil {
		return nil, err
	}

	if res, ok, err := s.resolveFromCache(ctx, userID, text, amounts, cats); err != nil {
		s.log.Warn("кэш-резолвер сломался, иду в модель", "err", err)
	} else if ok {
		s.log.Info("разбор без сети", "fast_path", true, "user_id", userID)
		return res, nil
	}

	raw, err := s.llm.Parse(ctx, text, cats)
	if err != nil {
		s.log.Warn("модель не разобрала сообщение", "fast_path", false, "user_id", userID,
			"kind", ErrKind(err), "err", err)
		return nil, err
	}

	items := Validate(raw, text, cats, s.log)
	if len(items) == 0 {
		s.log.Warn("после валидации не осталось элементов", "fast_path", false, "user_id", userID)
		return nil, ErrEmptyAfterValidation
	}
	s.log.Info("разбор моделью", "fast_path", false, "user_id", userID, "items", len(items))
	return &Result{Items: items, Source: SourceLLM}, nil
}

// ErrEmptyAfterValidation — модель ответила, но после проверок не осталось
// ни одного элемента. Дальше включается деградированный путь (§8).
var ErrEmptyAfterValidation = errors.New("после валидации не осталось элементов")

// categoryByName — вспомогательный поиск категории по имени, регистр не важен.
func categoryByName(cats []storage.Category, name string) *storage.Category {
	for i := range cats {
		if strings.EqualFold(cats[i].Name, name) {
			return &cats[i]
		}
	}
	return nil
}

// categoryByID — поиск категории по идентификатору.
func categoryByID(cats []storage.Category, id int32) *storage.Category {
	for i := range cats {
		if cats[i].ID == id {
			return &cats[i]
		}
	}
	return nil
}
