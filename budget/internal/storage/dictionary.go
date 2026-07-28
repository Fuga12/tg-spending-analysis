package storage

import "context"

// Category — строка таблицы categories. Список фиксирован миграцией 0002,
// из него же собирается enum JSON-схемы для модели (§6).
type Category struct {
	ID                 int32
	Name               string
	DefaultBeneficiary string
	SortOrder          int

	// Hint уходит в описание категории внутри JSON-схемы: пояснения заметно
	// поднимают точность разбора (plan.md §6).
	Hint string
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
		select id, name, default_beneficiary, sort_order, hint
		from categories order by sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.DefaultBeneficiary, &c.SortOrder, &c.Hint); err != nil {
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

// UpdateCategory правит имя и подсказку. Добавлять и удалять категории
// нельзя: их ровно четырнадцать (plan.md §14), а список уходит в enum схемы.
func (s *Store) UpdateCategory(ctx context.Context, id int32, name, hint, beneficiary string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update categories set name = $2, hint = $3, default_beneficiary = $4
		where id = $1`, id, name, hint, beneficiary)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// CreateCategory заводит новую категорию в конец списка.
//
// plan.md §14 запрещал больше четырнадцати — ограничение снято по решению
// заказчика. Цена известна: список уходит в enum JSON-схемы, и каждая
// категория делает промпт чуть длиннее, а выбор модели чуть труднее.
func (s *Store) CreateCategory(ctx context.Context, name, hint, beneficiary string) (Category, error) {
	c := Category{Name: name, Hint: hint, DefaultBeneficiary: beneficiary}
	err := s.pool.QueryRow(ctx, `
		insert into categories (name, default_beneficiary, sort_order, hint)
		values ($1, $2, (select coalesce(max(sort_order), 0) + 10 from categories), $3)
		returning id, sort_order`, name, beneficiary, hint).Scan(&c.ID, &c.SortOrder)
	return c, err
}

// ForgetLLMWords выбрасывает из личных словарей слова, привязанные моделью.
//
// Список категорий изменился — значит прежние догадки устарели: «пиво»,
// однажды разобранное в Продукты, иначе резолвилось бы туда вечно, даже
// после появления категории «Алкоголь». Ручные привязки не трогаем.
func (s *Store) ForgetLLMWords(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `delete from word_map where source = 'llm'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
