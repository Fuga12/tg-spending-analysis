package storage

import "context"

// Category — строка таблицы categories. Список фиксирован миграцией 0002,
// из него же собирается enum JSON-схемы для модели (§6).
type Category struct {
	ID                 int32
	Name               string
	DefaultBeneficiary string
	SortOrder          int
}

// WordHit — как слово разрешилось в категорию.
type WordHit struct {
	Word        string
	CategoryID  int32
	Beneficiary string // пусто — брать default_beneficiary категории
	Source      string // manual | llm | seed
	Hits        int
}

// Источники разрешения слова, в порядке убывания приоритета (§5).
const (
	SourceManual = "manual"
	SourceLLM    = "llm"
	SourceSeed   = "seed"
)

// Categories возвращает все категории в порядке отображения.
func (s *Store) Categories(ctx context.Context) ([]Category, error) {
	rows, err := s.pool.Query(ctx, `
		select id, name, default_beneficiary, sort_order
		from categories order by sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.DefaultBeneficiary, &c.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LookupWords разрешает слова через личный кэш пользователя, при промахе —
// через общую затравку. Слова, которых нет нигде, в ответе отсутствуют.
func (s *Store) LookupWords(ctx context.Context, userID int64, words []string) (map[string]WordHit, error) {
	if len(words) == 0 {
		return map[string]WordHit{}, nil
	}

	rows, err := s.pool.Query(ctx, `
		select word, category_id, coalesce(beneficiary, ''), source, hits
		from word_map
		where user_id = $1 and word = any($2)
		union all
		select word, category_id, coalesce(beneficiary, ''), 'seed', 0
		from word_seed
		where word = any($2)`, userID, words)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]WordHit, len(words))
	for rows.Next() {
		var h WordHit
		if err := rows.Scan(&h.Word, &h.CategoryID, &h.Beneficiary, &h.Source, &h.Hits); err != nil {
			return nil, err
		}
		// Личный кэш всегда важнее общей затравки (§5).
		if prev, ok := out[h.Word]; ok && prev.Source != SourceSeed {
			continue
		}
		out[h.Word] = h
	}
	return out, rows.Err()
}

// UpsertWord запоминает слово в личном кэше. Ручная правка пользователя
// приоритетнее ответа модели и не перезатирается (§8).
func (s *Store) UpsertWord(ctx context.Context, userID int64, word string, categoryID int32, beneficiary, source string) error {
	var ben any
	if beneficiary != "" {
		ben = beneficiary
	}
	_, err := s.pool.Exec(ctx, `
		insert into word_map (word, user_id, category_id, beneficiary, source)
		values ($1, $2, $3, $4, $5)
		on conflict (word, user_id) do update
		set category_id = case when word_map.source = 'manual' and excluded.source <> 'manual'
		                       then word_map.category_id else excluded.category_id end,
		    beneficiary = case when word_map.source = 'manual' and excluded.source <> 'manual'
		                       then word_map.beneficiary else excluded.beneficiary end,
		    source      = case when word_map.source = 'manual' and excluded.source <> 'manual'
		                       then word_map.source else excluded.source end,
		    hits        = word_map.hits + 1,
		    updated_at  = now()`,
		word, userID, categoryID, ben, source)
	return err
}
