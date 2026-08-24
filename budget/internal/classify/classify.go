// Package classify превращает сообщение пользователя в набор транзакций.
//
// Порядок один и тот же всегда: сначала кэш-резолвер, он отвечает
// мгновенно и без сети; если не сработал — LLM; что бы ни вернула
// модель, результат проходит детерминированную валидацию.
package classify

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
	"budget/internal/tokens"
)

// Тип операции.
const (
	KindExpense  = "expense"
	KindIncome   = "income"
	KindTransfer = "transfer"
)

// FallbackCategory — куда падает всё, что не разобрали.
//
// Ищется по ключу шаблона, а не по имени: группа вправе переименовать
// «Прочее» во что угодно, и поиск по имени после этого возвращал бы nil,
// оставляя запись в очереди воркера навсегда. Nil означает, что категорию
// с этим ключом группа удалила — тогда трата останется без категории.
func FallbackCategory(cats []storage.Category) *storage.Category {
	for i := range cats {
		if cats[i].TemplateKey == storage.TemplateOther {
			return &cats[i]
		}
	}
	return nil
}

// Item — одна разобранная трата, готовая к записи в transactions.
type Item struct {
	Amount      decimal.Decimal
	Description string
	CategoryID  *int32 // nil — без категории
	Kind        string
	DaysAgo     int

	// Recipients — на кого потрачено, member_id. Пустой список означает «на
	// всю группу» и появляется, только если так сказано в сообщении: молчание
	// про получателя разрешается умолчанием категории, а не общей корзиной.
	Recipients []int64

	// Words — значимые слова описания, их запоминает кэш после успешного
	// разбора. Для деградированного результата пусто.
	Words []string

	// NeedsClassification — запись сохранена, но категорию поставит воркер
	// добора: LLM не ответил или был недоступен.
	NeedsClassification bool
}

// Source — каким путём получен результат. Нужен для метрики скорости
// и для команды /лимит.
type Source string

const (
	SourceCache    Source = "cache"
	SourceLLM      Source = "llm"
	SourceDegraded Source = "degraded"
)

// Причины деградации. Различать их обязан воркер добора: запись, которой не
// хватило сети, надо повторить позже, а запись, которую модель разбирать
// отказывается, — закрыть, иначе она будет возвращаться вечно.
const (
	ReasonNoNetwork  = "сеть недоступна"
	ReasonNoAnswer   = "модель не ответила"
	ReasonUnusable   = "после валидации не осталось элементов"
	ReasonNoCategory = "категории недоступны"
	ReasonNoMembers  = "состав группы недоступен"
	ReasonGroupQuota = "потолок токенов группы исчерпан"
)

// Result — итог разбора одного сообщения.
type Result struct {
	Items  []Item
	Source Source

	// Reason заполняется только для деградированного результата.
	Reason string
}

// ErrNoAmount — в сообщении нет ни одного числа, сохранять нечего.
var ErrNoAmount = errors.New("в сообщении нет суммы")

// Dict — то, что классификатору нужно от хранилища группы.
type Dict interface {
	Categories(ctx context.Context) ([]storage.Category, error)
	Members(ctx context.Context) ([]storage.Member, error)
	LookupWords(ctx context.Context, userID int64, words []string) (map[string]storage.WordHit, error)
}

// Scope — чьё сообщение разбираем.
//
// Хранилище приезжает снаружи, а не лежит в Service: категории, участники,
// личные привязки и счётчик токенов принадлежат группе, а Service один на
// процесс. Так подсунуть сюда данные чужой группы можно только намеренно.
type Scope struct {
	// Payer — кто пишет. Его словарь читает быстрый путь, его именем модель
	// понимает «себе», на него уходят траты без названного получателя.
	Payer storage.Member
	Dict  Dict
	// Usage — куда писать расход токенов этой группы. В боевом коде это тот
	// же *storage.GroupStore, что и Dict.
	Usage UsageRecorder
	// Quota — потолок токенов этой группы. Nil означает «без потолка».
	//
	// Общий потолок защищает сервис от собственных багов, а групповой — от
	// чужих групп: пока бот открыт, их сообщения жгут один и тот же грант,
	// и без разбивки первая же активная группа выбирает его целиком.
	Quota Budgetable
}

// Request — всё, что модели нужно знать о группе, чтобы разобрать сообщение.
//
// Состав группы едет вместе с текстом: получателя нельзя назвать иначе как по
// имени, а имена у каждой группы свои. Отсюда же и Payer — без него модель не
// понимает «себе».
type Request struct {
	Text   string
	Cats   []storage.Category
	Roster *Roster
	Payer  storage.Member
}

// LLM — обращение к модели. Возвращает разобранные моделью элементы как есть,
// без валидации: проверять их — дело validate.go.
//
// Счётчик токенов передаётся вызовом, а не лежит в клиенте: клиент один на
// процесс, а расход считается по группам.
type LLM interface {
	Parse(ctx context.Context, usage UsageRecorder, req Request) ([]RawItem, error)
}

// Breakable — предохранители, стоящие перед сетевым вызовом.
type Breakable interface {
	Allow() bool
	Record(err error)
}

// Budgetable — месячный потолок токенов.
type Budgetable interface {
	Allow(ctx context.Context) bool
}

// Service связывает кэш, предохранители, модель и валидацию.
type Service struct {
	llm     LLM
	breaker Breakable
	budget  Budgetable
	log     *slog.Logger

	cacheHits atomic.Int64
	llmCalls  atomic.Int64
	degraded  atomic.Int64
}

func NewService(llm LLM, breaker Breakable, budget Budgetable, log *slog.Logger) *Service {
	return &Service{llm: llm, breaker: breaker, budget: budget, log: log}
}

// Classify разбирает сообщение пользователя.
//
// Единственная ошибка, которую метод возвращает, — ErrNoAmount: сохранять
// тогда нечего. Во всех остальных случаях результат есть, пусть и
// деградированный. Потеря записи из-за недоступности API недопустима.
func (s *Service) Classify(ctx context.Context, sc Scope, text string) (*Result, error) {
	// Ноль и минус тратой быть не могут — в базе стоит check (amount > 0),
	// и деградированный путь такую запись всё равно не сохранил бы.
	amounts := positive(tokens.Extract(text))
	if len(amounts) == 0 {
		return nil, ErrNoAmount
	}

	cats, err := sc.Dict.Categories(ctx)
	if err != nil {
		// Без категорий нельзя ни в кэш, ни в модель — остаётся деградация.
		s.log.Error("не смог прочитать категории", "err", err)
		return s.degrade(sc, text, amounts, ReasonNoCategory), nil
	}
	members, err := sc.Dict.Members(ctx)
	if err != nil {
		// Без состава группы получателя не назвать даже по имени.
		s.log.Error("не смог прочитать участников", "err", err)
		return s.degrade(sc, text, amounts, ReasonNoMembers), nil
	}
	req := Request{Text: text, Cats: cats, Roster: NewRoster(members), Payer: sc.Payer}

	if res, ok, err := s.resolveFromCache(ctx, sc, amounts, req); err != nil {
		s.log.Warn("кэш-резолвер сломался, иду в модель", "err", err)
	} else if ok {
		s.cacheHits.Add(1)
		s.log.Info("разбор без сети", "fast_path", true, "user_id", sc.Payer.UserID)
		return res, nil
	}

	// Бюджет проверяется первым: Breaker.Allow расходует пробную попытку,
	// и тратить её на вызов, которого всё равно не будет, нельзя.
	if !s.budget.Allow(ctx) {
		return s.degrade(sc, text, amounts, ReasonNoNetwork), nil
	}
	if sc.Quota != nil && !sc.Quota.Allow(ctx) {
		s.log.Warn("месячный потолок группы исчерпан", "user_id", sc.Payer.UserID)
		return s.degrade(sc, text, amounts, ReasonGroupQuota), nil
	}
	if !s.breaker.Allow() {
		return s.degrade(sc, text, amounts, ReasonNoNetwork), nil
	}

	raw, err := s.llm.Parse(ctx, sc.Usage, req)
	s.breaker.Record(err)
	if err != nil {
		s.log.Warn("модель не разобрала сообщение", "fast_path", false, "user_id", sc.Payer.UserID,
			"kind", ErrKind(err), "err", err)
		return s.degrade(sc, text, amounts, ReasonNoAnswer), nil
	}

	items := Validate(raw, req, s.log)
	if len(items) == 0 {
		s.log.Warn("после валидации не осталось элементов", "fast_path", false, "user_id", sc.Payer.UserID)
		return s.degrade(sc, text, amounts, ReasonUnusable), nil
	}
	s.llmCalls.Add(1)
	s.log.Info("разбор моделью", "fast_path", false, "user_id", sc.Payer.UserID, "items", len(items))
	return &Result{Items: items, Source: SourceLLM}, nil
}

// degrade собирает запись, которую можно сохранить без модели: сумма — первая
// из найденных, описание — текст без суммы, категорию поставит воркер.
func (s *Service) degrade(sc Scope, text string, amounts []decimal.Decimal, reason string) *Result {
	description := Describe(text)
	if description == "" {
		description = trimTo(strings.TrimSpace(text), 64)
	}

	s.degraded.Add(1)
	s.log.Warn("деградированная запись", "причина", reason, "сумма", amounts[0].String())
	return &Result{
		Source: SourceDegraded,
		Reason: reason,
		Items: []Item{{
			Amount:      amounts[0],
			Description: description,
			CategoryID:  nil,
			Kind:        KindExpense,
			DaysAgo:     0,
			// Получатель — плательщик: это самое безобидное предположение.
			// Записать трату на всю группу значит утверждать то, чего никто
			// не говорил, а воркер добора потом поправит.
			Recipients:          []int64{sc.Payer.ID},
			NeedsClassification: true,
		}},
	}
}

// positive оставляет только суммы больше нуля.
func positive(amounts []decimal.Decimal) []decimal.Decimal {
	out := make([]decimal.Decimal, 0, len(amounts))
	for _, a := range amounts {
		if a.IsPositive() {
			out = append(out, a)
		}
	}
	return out
}

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

// Stats — сколько сообщений каким путём разобрано с момента запуска. Нужны
// команде /лимит: доля быстрого пути — это метрика скорости.
type Stats struct {
	Cache    int64
	LLM      int64
	Degraded int64
}

// Total — всего разобранных сообщений.
func (s Stats) Total() int64 { return s.Cache + s.LLM + s.Degraded }

// Stats отдаёт накопленные счётчики.
func (s *Service) Stats() Stats {
	return Stats{
		Cache:    s.cacheHits.Load(),
		LLM:      s.llmCalls.Load(),
		Degraded: s.degraded.Load(),
	}
}
