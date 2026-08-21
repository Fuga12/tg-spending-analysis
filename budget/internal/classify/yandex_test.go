package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"budget/internal/storage"
)

// recordedUsage — строка, которая ушла бы в llm_usage.
type recordedUsage struct {
	Model      string
	Prompt     int
	Completion int
	OK         bool
	ErrorKind  string
}

type fakeUsage struct {
	mu   sync.Mutex
	rows []recordedUsage
}

func (f *fakeUsage) RecordUsage(_ context.Context, model string, prompt, completion int, ok bool, errorKind string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, recordedUsage{model, prompt, completion, ok, errorKind})
	return nil
}

func (f *fakeUsage) all() []recordedUsage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedUsage(nil), f.rows...)
}

const okResponse = `{
  "choices":[{"message":{"content":"{\"items\":[{\"amount\":1200,\"description\":\"пятёрочка\",\"category\":\"Продукты\",\"kind\":\"expense\",\"days_ago\":1}]}"}}],
  "usage":{"prompt_tokens":650,"completion_tokens":48}
}`

func newTestYandex(t *testing.T, h http.HandlerFunc) (*Yandex, *fakeUsage, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	usage := &fakeUsage{}
	y := NewYandex(YandexConfig{
		BaseURL:  srv.URL,
		APIKey:   "test-key",
		FolderID: "test-folder",
		Model:    "yandexgpt/rc",
		Timeout:  2 * time.Second,
	}, quietLog())
	return y, usage, srv
}

func TestYandexRequestShape(t *testing.T) {
	var got struct {
		Model          string `json:"model"`
		Temperature    int    `json:"temperature"`
		MaxTokens      int    `json:"max_tokens"`
		Stream         bool   `json:"stream"`
		Messages       []struct{ Role, Content string }
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string `json:"name"`
				Schema struct {
					Properties struct {
						Items struct {
							Items struct {
								Properties struct {
									Category struct {
										Enum []string `json:"enum"`
									} `json:"category"`
								} `json:"properties"`
								Required []string `json:"required"`
							} `json:"items"`
						} `json:"items"`
					} `json:"properties"`
				} `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	var headers http.Header

	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(okResponse))
	})

	if _, err := y.Parse(context.Background(), usage, "вчера пятёрочка 1200", testCategories()); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if headers.Get("Authorization") != "Api-Key test-key" {
		t.Errorf("Authorization = %q, у Яндекса схема Api-Key", headers.Get("Authorization"))
	}
	if headers.Get("OpenAI-Project") != "test-folder" {
		t.Errorf("OpenAI-Project = %q", headers.Get("OpenAI-Project"))
	}
	if headers.Get("x-data-logging-enabled") != "false" {
		t.Errorf("x-data-logging-enabled = %q, через API едет история личных трат",
			headers.Get("x-data-logging-enabled"))
	}
	if got.Model != "gpt://test-folder/yandexgpt/rc" {
		t.Errorf("model = %q", got.Model)
	}
	if got.Temperature != 0 || got.MaxTokens != 500 || got.Stream {
		t.Errorf("temperature/max_tokens/stream = %d/%d/%v, ожидалось 0/500/false",
			got.Temperature, got.MaxTokens, got.Stream)
	}
	if len(got.Messages) != 2 || got.Messages[1].Content != "вчера пятёрочка 1200" {
		t.Errorf("сообщение пользователя должно уходить как есть, без обёрток: %+v", got.Messages)
	}
	if !strings.Contains(got.Messages[0].Content, "\"items\":[{") {
		t.Error("в системном промпте нет few-shot примеров")
	}

	// Enum категорий генерируется из таблицы, а не из константы (§6).
	enum := got.ResponseFormat.JSONSchema.Schema.Properties.Items.Items.Properties.Category.Enum
	if len(enum) != len(testCategories()) || enum[0] != "Продукты" {
		t.Errorf("enum категорий = %v, ожидался список из таблицы categories", enum)
	}
	if got.ResponseFormat.Type != "json_schema" {
		t.Errorf("response_format.type = %q", got.ResponseFormat.Type)
	}
	// Получателя в схеме больше нет: модель его не определяет (фаза 2).
	if n := len(got.ResponseFormat.JSONSchema.Schema.Properties.Items.Items.Required); n != 5 {
		t.Errorf("обязательных полей в схеме %d, ожидалось 5", n)
	}
}

func TestSystemPromptListsCategoriesWithHints(t *testing.T) {
	// Подсказка категории должна доезжать до модели читаемым списком:
	// «пиво» уходило в Продукты мимо живой категории «Алкоголь», пока
	// подсказки слипались в одну строку описания поля схемы.
	var got struct {
		Messages []struct{ Role, Content string }
	}
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(okResponse))
	})

	cats := append(testCategories(),
		storage.Category{ID: 9, Name: "Алкоголь", Hint: "пиво, вино и тп"})
	if _, err := y.Parse(context.Background(), usage, "пиво 4000", cats); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	system := got.Messages[0].Content
	if !strings.Contains(system, "- Алкоголь — пиво, вино и тп") {
		t.Errorf("в системном промпте нет категории с подсказкой:\n%s", system)
	}
	if !strings.Contains(system, "- Такси\n") {
		t.Error("категория без подсказки должна попадать в список одним именем")
	}
}

func TestYandexParsesItemsAndRecordsUsage(t *testing.T) {
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(okResponse))
	})

	items, err := y.Parse(context.Background(), usage, "вчера пятёрочка 1200", testCategories())
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(items) != 1 || items[0].Amount.String() != "1200" || items[0].DaysAgo != 1 {
		t.Fatalf("разобрано неверно: %+v", items)
	}

	rows := usage.all()
	if len(rows) != 1 {
		t.Fatalf("строк расхода %d, ожидалась одна", len(rows))
	}
	if rows[0].Prompt != 650 || rows[0].Completion != 48 || !rows[0].OK {
		t.Errorf("расход записан неверно: %+v", rows[0])
	}
	if rows[0].Model != "gpt://test-folder/yandexgpt/rc" {
		t.Errorf("модель в расходе = %q", rows[0].Model)
	}
}

func TestYandexRetriesOnce5xx(t *testing.T) {
	var calls int
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := y.Parse(context.Background(), usage, "600 лимонад", testCategories())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if ErrKind(err) != storage.ErrKindHTTP {
		t.Errorf("вид ошибки = %q, ожидался http", ErrKind(err))
	}
	if calls != 2 {
		t.Errorf("запросов %d, ожидались две попытки", calls)
	}
	// Каждая попытка — своя строка расхода, даже неуспешная (§6).
	if rows := usage.all(); len(rows) != 2 || rows[0].OK || rows[0].ErrorKind != storage.ErrKindHTTP {
		t.Errorf("расход записан неверно: %+v", rows)
	}
}

func TestYandexDoesNotRetryQuota(t *testing.T) {
	var calls int
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := y.Parse(context.Background(), usage, "600 лимонад", testCategories())
	if ErrKind(err) != storage.ErrKindQuota {
		t.Errorf("вид ошибки = %q, ожидался quota", ErrKind(err))
	}
	if calls != 1 {
		t.Errorf("запросов %d — ошибку квоты повтор не лечит (§6)", calls)
	}
	if rows := usage.all(); len(rows) != 1 || rows[0].ErrorKind != storage.ErrKindQuota {
		t.Errorf("расход записан неверно: %+v", rows)
	}
}

func TestYandexQuotaDetectedByBody(t *testing.T) {
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"quota exceeded for folder"}`))
	})

	_, err := y.Parse(context.Background(), usage, "600 лимонад", testCategories())
	if ErrKind(err) != storage.ErrKindQuota {
		t.Errorf("вид ошибки = %q, упоминание квоты в теле — тоже quota (§7)", ErrKind(err))
	}
}

func TestYandexBrokenContentIsSchemaError(t *testing.T) {
	var calls int
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"конечно! вот ваши траты"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	})

	_, err := y.Parse(context.Background(), usage, "600 лимонад", testCategories())
	if ErrKind(err) != storage.ErrKindSchema {
		t.Errorf("вид ошибки = %q, ожидался schema", ErrKind(err))
	}
	if calls != 1 {
		t.Errorf("запросов %d — ответ не по схеме ретраем не лечится (§7)", calls)
	}
	// Токены потрачены, значит записаны — но вызов неуспешный, и вид ошибки
	// должен быть виден в /лимит.
	rows := usage.all()
	if len(rows) != 1 || rows[0].Prompt != 10 || rows[0].Completion != 5 {
		t.Fatalf("расход записан неверно: %+v", rows)
	}
	if rows[0].OK || rows[0].ErrorKind != storage.ErrKindSchema {
		t.Errorf("строка расхода = %+v, ожидались ok=false и error_kind=schema", rows[0])
	}
}

func TestYandexEmptyChoicesIsSchemaError(t *testing.T) {
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":0}}`))
	})

	_, err := y.Parse(context.Background(), usage, "600 лимонад", testCategories())
	if ErrKind(err) != storage.ErrKindSchema {
		t.Errorf("вид ошибки = %q, ожидался schema", ErrKind(err))
	}
	if rows := usage.all(); len(rows) != 1 || rows[0].OK || rows[0].ErrorKind != storage.ErrKindSchema {
		t.Errorf("расход записан неверно: %+v", rows)
	}
}

func TestYandex5xxWithLimitInBodyStaysHTTP(t *testing.T) {
	// «rate limit» в теле пятисотки — это поломка сервиса, а не исчерпанная
	// квота: иначе ретрая не будет, а breaker откроется на полчаса (§7).
	var calls int
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"rate limit, try later"}`))
	})

	_, err := y.Parse(context.Background(), usage, "600 лимонад", testCategories())
	if ErrKind(err) != storage.ErrKindHTTP {
		t.Errorf("вид ошибки = %q, ожидался http", ErrKind(err))
	}
	if calls != 2 {
		t.Errorf("запросов %d, ожидались две попытки", calls)
	}
}

func TestYandexCancelIsNotServiceError(t *testing.T) {
	// Отмена снаружи (шатдаун, уход пользователя) не должна засчитываться
	// в счётчик ошибок breaker.
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(okResponse))
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := y.Parse(ctx, usage, "600 лимонад", testCategories())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if rows := usage.all(); len(rows) != 1 || rows[0].ErrorKind != storage.ErrKindOther {
		t.Errorf("расход записан как %+v, ожидался вид other", rows)
	}
}

func TestYandexTimeout(t *testing.T) {
	// Отвечаем заведомо позже таймаута клиента, но не бесконечно: иначе
	// httptest.Server.Close будет ждать обработчик и тест зависнет.
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(okResponse))
	})
	y.timeout = 50 * time.Millisecond

	_, err := y.Parse(context.Background(), usage, "600 лимонад", testCategories())
	if ErrKind(err) != storage.ErrKindTimeout {
		t.Errorf("вид ошибки = %q, ожидался timeout", ErrKind(err))
	}
	if rows := usage.all(); len(rows) != 2 {
		t.Errorf("строк расхода %d, ожидались две попытки", len(rows))
	}
}

func TestYandexBadKeyCountsTowardsBreaker(t *testing.T) {
	// Неверный ключ: повторять бессмысленно, но копиться в breaker обязано,
	// иначе бот долбится в сеть на каждое сообщение (§13, проверка фазы 3).
	var calls int
	y, usage, _ := newTestYandex(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"The token is invalid"}}`))
	})

	_, err := y.Parse(context.Background(), usage, "600 лимонад", testCategories())
	if ErrKind(err) != storage.ErrKindHTTP {
		t.Errorf("вид ошибки = %q, ожидался http", ErrKind(err))
	}
	if calls != 1 {
		t.Errorf("запросов %d — неверный ключ повтором не лечится", calls)
	}

	b := NewBreaker(time.Minute, quietLog())
	for i := 0; i < 3; i++ {
		b.Record(err)
	}
	if b.Allow() {
		t.Error("после трёх отказов с неверным ключом breaker должен закрыть сеть")
	}
}
