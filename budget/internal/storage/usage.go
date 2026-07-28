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

// UsageErrors — разбивка неуспешных вызовов по виду ошибки за текущий месяц,
// для команды /лимит (§7).
func (s *Store) UsageErrors(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		select coalesce(error_kind, 'other'), count(*)
		from llm_usage
		where not ok and created_at >= date_trunc('month', now())
		group by 1
		order by 2 desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var (
			kind  string
			count int
		)
		if err := rows.Scan(&kind, &count); err != nil {
			return nil, err
		}
		out[kind] = count
	}
	return out, rows.Err()
}
