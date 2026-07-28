package storage

import "context"

// Виды ошибок обращения к LLM (§7). Пишутся в llm_usage.error_kind.
const (
	ErrKindTimeout = "timeout"
	ErrKindQuota   = "quota"
	ErrKindHTTP    = "http"
	ErrKindSchema  = "schema"
	ErrKindOther   = "other"
)

// MonthUsage — расход за календарный месяц.
type MonthUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Calls            int
	Failed           int
}

// MonthlyUsage считает расход с начала текущего месяца. Границу задаёт
// date_trunc, поэтому первого числа счётчик обнуляется сам (§7).
func (s *Store) MonthlyUsage(ctx context.Context) (MonthUsage, error) {
	var u MonthUsage
	err := s.pool.QueryRow(ctx, `
		select coalesce(sum(prompt_tokens), 0),
		       coalesce(sum(completion_tokens), 0),
		       coalesce(sum(prompt_tokens + completion_tokens), 0),
		       count(*),
		       count(*) filter (where not ok)
		from llm_usage
		where created_at >= date_trunc('month', now())`).
		Scan(&u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.Calls, &u.Failed)
	return u, err
}

// RecordUsage пишет строку про вызов LLM — успешный или нет. Без этой таблицы
// невозможно понять, куда уходит грант (§6).
func (s *Store) RecordUsage(ctx context.Context, model string, promptTokens, completionTokens int, ok bool, errorKind string) error {
	var kind any
	if errorKind != "" {
		kind = errorKind
	}
	_, err := s.pool.Exec(ctx, `
		insert into llm_usage (model, prompt_tokens, completion_tokens, ok, error_kind)
		values ($1, $2, $3, $4, $5)`,
		model, promptTokens, completionTokens, ok, kind)
	return err
}
