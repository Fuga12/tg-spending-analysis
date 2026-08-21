package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"budget/internal/storage"
)

// RawItem — элемент ровно в том виде, в каком его вернула модель.
// Никаких проверок: их делает validate.go.
type RawItem struct {
	Amount      json.Number `json:"amount"`
	Description string      `json:"description"`
	Category    string      `json:"category"`
	Kind        string      `json:"kind"`
	DaysAgo     int         `json:"days_ago"`

	// Recipients — имена участников, которым досталась трата, или «на всех».
	Recipients []string `json:"recipients"`

	// RecipientsStated — сказано ли в сообщении, кому трата, прямым текстом.
	// Если нет, получателя даёт не догадка модели, а умолчание категории:
	// свои привычки люди знают лучше (см. validate.go).
	RecipientsStated bool `json:"recipients_stated"`
}

// UsageRecorder — куда писать расход токенов. После каждого вызова,
// успешного или нет (§6). Принадлежит группе: расход считается по ней.
type UsageRecorder interface {
	RecordUsage(ctx context.Context, model string, promptTokens, completionTokens int, ok bool, errorKind string) error
}

// Error — ошибка обращения к модели с видом из §7.
type Error struct {
	Kind string
	// Retryable — есть ли смысл в повторе. Квоту и неверный ключ повтор не
	// лечит, поломку сервиса и таймаут — лечит.
	Retryable bool
	Err       error
}

func (e *Error) Error() string { return e.Kind + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// ErrKind достаёт вид ошибки; для не-LLM ошибок — "other".
func ErrKind(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return storage.ErrKindOther
}

// Yandex — клиент Yandex AI Studio. Голый net/http: у Яндекса схема
// авторизации Api-Key, а не Bearer, и SDK под неё не подходит (§0).
type Yandex struct {
	client   *http.Client
	baseURL  string
	apiKey   string
	folderID string
	model    string
	timeout  time.Duration
	log      *slog.Logger
}

// YandexConfig — настройки клиента.
type YandexConfig struct {
	BaseURL  string
	APIKey   string
	FolderID string
	Model    string
	Timeout  time.Duration
}

func NewYandex(cfg YandexConfig, log *slog.Logger) *Yandex {
	return &Yandex{
		client:   &http.Client{},
		baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:   cfg.APIKey,
		folderID: cfg.FolderID,
		model:    cfg.Model,
		timeout:  cfg.Timeout,
		log:      log,
	}
}

// ModelURI — идентификатор модели в формате Яндекса (§6).
func (y *Yandex) ModelURI() string {
	return "gpt://" + y.folderID + "/" + y.model
}

// Parse отправляет сообщение модели. Один ретрай при 5xx и таймауте,
// backoff 500 мс. Ошибку квоты ретрай не лечит, поэтому для неё повтора нет.
func (y *Yandex) Parse(ctx context.Context, usage UsageRecorder, req Request) ([]RawItem, error) {
	const retryBackoff = 500 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryBackoff):
			}
		}

		items, err := y.call(ctx, usage, req)
		if err == nil {
			return items, nil
		}
		lastErr = err

		var e *Error
		if errors.As(err, &e) && e.Retryable {
			y.log.Warn("модель не ответила, повторяю", "err", err, "attempt", attempt+1)
			continue
		}
		return nil, err
	}
	return nil, lastErr
}

func (y *Yandex) call(ctx context.Context, usage UsageRecorder, req Request) ([]RawItem, error) {
	body, err := json.Marshal(y.request(req))
	if err != nil {
		return nil, &Error{Kind: storage.ErrKindOther, Err: err}
	}

	callCtx, cancel := context.WithTimeout(ctx, y.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, y.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: storage.ErrKindOther, Err: err}
	}
	httpReq.Header.Set("Authorization", "Api-Key "+y.apiKey)
	httpReq.Header.Set("OpenAI-Project", y.folderID)
	httpReq.Header.Set("Content-Type", "application/json")
	// Через API едет история личных трат — логирование на стороне Яндекса
	// должно быть выключено (§6).
	httpReq.Header.Set("x-data-logging-enabled", "false")

	resp, err := y.client.Do(httpReq)
	if err != nil {
		kind := transportErrKind(ctx, callCtx, err)
		y.record(ctx, usage, 0, 0, false, kind)
		return nil, &Error{Kind: kind, Retryable: retryableKind(kind), Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		kind := transportErrKind(ctx, callCtx, err)
		y.record(ctx, usage, 0, 0, false, kind)
		return nil, &Error{Kind: kind, Retryable: retryableKind(kind), Err: err}
	}

	if resp.StatusCode != http.StatusOK {
		kind := httpErrKind(resp.StatusCode, raw)
		y.record(ctx, usage, 0, 0, false, kind)
		return nil, &Error{
			Kind: kind,
			// Повторяем только поломку сервиса. Неверный ключ и битый запрос
			// повтором не лечатся, но в счётчик breaker идут: иначе бот будет
			// долбиться в сеть на каждое сообщение (§13, проверка фазы 3).
			Retryable: kind == storage.ErrKindHTTP && resp.StatusCode >= 500,
			Err:       fmt.Errorf("HTTP %d: %s", resp.StatusCode, trimForLog(raw)),
		}
	}

	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		y.record(ctx, usage, 0, 0, false, storage.ErrKindSchema)
		return nil, &Error{Kind: storage.ErrKindSchema, Err: err}
	}

	// Токены потрачены и записываются в любом случае — но вместе с настоящим
	// исходом вызова. Иначе schema-ошибки, самый частый режим поломки модели,
	// не видны ни в /лимит, ни в счётчике неуспешных.
	prompt, completion := envelope.Usage.PromptTokens, envelope.Usage.CompletionTokens

	if len(envelope.Choices) == 0 {
		y.record(ctx, usage, prompt, completion, false, storage.ErrKindSchema)
		return nil, &Error{Kind: storage.ErrKindSchema, Err: errors.New("модель вернула пустой ответ")}
	}

	var payload struct {
		Items []RawItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(envelope.Choices[0].Message.Content), &payload); err != nil {
		y.record(ctx, usage, prompt, completion, false, storage.ErrKindSchema)
		return nil, &Error{Kind: storage.ErrKindSchema, Err: fmt.Errorf("содержимое ответа не по схеме: %w", err)}
	}

	y.record(ctx, usage, prompt, completion, true, "")
	return payload.Items, nil
}

// request собирает тело запроса: строгий JSON-схемный ответ, нулевая
// температура, без стриминга и без истории диалога (§6).
func (y *Yandex) request(req Request) map[string]any {
	return map[string]any{
		"model":       y.ModelURI(),
		"temperature": 0,
		"max_tokens":  500,
		"stream":      false,
		"messages": []map[string]string{
			{"role": "system", "content": SystemPrompt(req)},
			{"role": "user", "content": req.Text},
		},
		"response_format": responseFormat(req),
	}
}

// responseFormat — JSON-схема ответа. Списки категорий и участников берутся
// из состояния группы, а не из константы: иначе схема и БД разъедутся при
// первом же изменении (§6).
func responseFormat(req Request) map[string]any {
	cats := req.Cats
	names := make([]string, 0, len(cats))
	hints := make([]string, 0, len(cats))
	for _, c := range cats {
		names = append(names, c.Name)
		if c.Hint != "" {
			hints = append(hints, c.Name+" — "+c.Hint)
		}
	}

	// Подсказки уходят прямо в описание поля: пояснения внутри схемы заметно
	// поднимают точность, и экономить на них незачем (plan.md §6).
	categoryDesc := "Категория траты"
	if len(hints) > 0 {
		categoryDesc += ". " + strings.Join(hints, "; ")
	}

	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "transactions",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"items": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"amount": map[string]any{
									"type":        "number",
									"description": "Сумма траты ровно так, как записана в сообщении",
								},
								"description": map[string]any{
									"type":        "string",
									"description": "Суть траты, 1-3 слова, без суммы",
								},
								"category": map[string]any{
									"type":        "string",
									"description": categoryDesc,
									"enum":        names,
								},
								"recipients": map[string]any{
									"type": "array",
									"description": "Кому досталась трата. Только имена участников " +
										"из списка или «" + Everyone + "», если трата общая",
									"items": map[string]any{
										"type": "string",
										"enum": req.Roster.Enum(),
									},
								},
								"recipients_stated": map[string]any{
									"type": "boolean",
									"description": "true, только если про получателя сказано прямо — " +
										"по имени, «себе», «нам», «всем». " +
										"Если это твоя догадка по смыслу — false",
								},
								"kind": map[string]any{
									"type":        "string",
									"description": "Тип операции",
									"enum":        []string{KindExpense, KindIncome, KindTransfer},
								},
								"days_ago": map[string]any{
									"type":        "integer",
									"description": "Сколько дней назад произошла трата, 0 = сегодня",
								},
							},
							"required": []string{
								"amount", "description", "category",
								"recipients", "recipients_stated", "kind", "days_ago",
							},
						},
					},
				},
				"required": []string{"items"},
			},
		},
	}
}
func (y *Yandex) record(ctx context.Context, usage UsageRecorder, prompt, completion int, ok bool, errorKind string) {
	// Запись расхода не должна пропасть из-за того, что истёк контекст запроса.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := usage.RecordUsage(recCtx, y.ModelURI(), prompt, completion, ok, errorKind); err != nil {
		y.log.Error("не записал расход токенов", "err", err)
	}
}

// transportErrKind различает свой таймаут, отмену снаружи и поломку сети.
// Отменённый пользователем или шатдауном запрос — не ошибка сервиса, и в
// счётчик breaker (§7) он попадать не должен.
func transportErrKind(parent, call context.Context, err error) string {
	switch {
	case parent.Err() != nil && errors.Is(parent.Err(), context.Canceled):
		return storage.ErrKindOther
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(call.Err(), context.DeadlineExceeded):
		return storage.ErrKindTimeout
	default:
		return storage.ErrKindHTTP
	}
}

// retryableKind — стоит ли повторять при ошибке транспорта.
func retryableKind(kind string) bool {
	return kind == storage.ErrKindTimeout || kind == storage.ErrKindHTTP
}

// httpErrKind различает исчерпанную квоту и обычную поломку сервиса (§7).
// Любой другой отказ сервиса — в том числе 401 при неверном ключе — считается
// http: такие ошибки должны копиться в breaker, иначе бот будет долбиться
// в сеть на каждое сообщение (§13).
func httpErrKind(status int, body []byte) string {
	if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
		return storage.ErrKindQuota
	}
	// 5xx — поломка сервиса, её лечит ретрай. Слово «limit» в тексте пятисотки
	// не должно превращать её в квоту и открывать breaker на полчаса.
	if status >= 500 {
		return storage.ErrKindHTTP
	}
	lower := strings.ToLower(string(body))
	for _, marker := range []string{"quota", "квот", "лимит", "limit exceeded", "insufficient"} {
		if strings.Contains(lower, marker) {
			return storage.ErrKindQuota
		}
	}
	return storage.ErrKindHTTP
}

func trimForLog(b []byte) string {
	const max = 300
	s := strings.TrimSpace(string(b))
	if len([]rune(s)) > max {
		return string([]rune(s)[:max]) + "…"
	}
	return s
}
