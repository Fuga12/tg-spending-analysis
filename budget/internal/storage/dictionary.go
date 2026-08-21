package storage

import "context"

// Category — строка таблицы categories. Список свой у каждой группы:
// раскатывается из шаблона при создании и дальше правится людьми.
type Category struct {
	ID        int32
	Name      string
	SortOrder int

	// Hint уходит в описание категории внутри JSON-схемы: пояснения заметно
	// поднимают точность разбора.
	Hint string

	// TemplateKey — из какой строки шаблона раскатана категория. Пусто у
	// заведённой людьми: затравка словаря общая на всех и ссылается на ключ
	// шаблона, а не на category_id конкретной группы.
	TemplateKey string

	// DefaultMemberID — адресат-человек, если он у категории есть: «Косметика —
	// Уле» верно и когда платит не Уля. Nil означает «на всю группу».
	DefaultMemberID *int64
}

// TemplateOther — ключ шаблона для категории, куда падает всё, что не
// разобрали. Именно ключ, а не имя: группа вправе переименовать «Прочее»
// во что угодно, и поиск по имени после этого перестал бы находить её.
const TemplateOther = "other"

// WordHit — как слово разрешилось в категорию.
type WordHit struct {
	Word       string
	CategoryID int32
	// MemberID — получатель, если слово помнит и его. Nil — брать умолчание
	// категории. Заполняется с фазы 2, пока всегда пуст.
	MemberID *int64
	Source   string // manual | llm | seed
	Hits     int
}

// Источники разрешения слова, в порядке убывания приоритета.
const (
	SourceManual = "manual"
	SourceLLM    = "llm"
	SourceSeed   = "seed"
)

// Categories возвращает категории группы в порядке отображения.
func (g *GroupStore) Categories(ctx context.Context) ([]Category, error) {
	rows, err := g.pool.Query(ctx, `
		select id, name, sort_order, hint, coalesce(template_key, ''), default_member_id
		from categories where group_id = $1
		order by sort_order, id`, g.groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.SortOrder, &c.Hint,
			&c.TemplateKey, &c.DefaultMemberID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LookupWords разрешает слова через личный словарь человека, при промахе —
// через общую затравку. Слова, которых нет нигде, в ответе отсутствуют.
//
// Затравка хранит ключ шаблона, поэтому её приходится приземлять на категории
// именно этой группы. Категорию, которую группа удалила, затравка не найдёт —
// и правильно: разрешать слово в чужую категорию хуже, чем не разрешать вовсе.
func (g *GroupStore) LookupWords(ctx context.Context, userID int64, words []string) (map[string]WordHit, error) {
	if len(words) == 0 {
		return map[string]WordHit{}, nil
	}

	rows, err := g.pool.Query(ctx, `
		select word, category_id, member_id, source, hits
		from word_map
		where group_id = $1 and user_id = $2 and word = any($3)
		union all
		select s.word, c.id, null, 'seed', 0
		from word_seed s
		join categories c on c.group_id = $1 and c.template_key = s.template_key
		where s.word = any($3)`, g.groupID, userID, words)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]WordHit, len(words))
	for rows.Next() {
		var h WordHit
		if err := rows.Scan(&h.Word, &h.CategoryID, &h.MemberID, &h.Source, &h.Hits); err != nil {
			return nil, err
		}
		// Личный словарь всегда важнее общей затравки.
		if prev, ok := out[h.Word]; ok && prev.Source != SourceSeed {
			continue
		}
		out[h.Word] = h
	}
	return out, rows.Err()
}

// UpsertWord запоминает слово в личном словаре. Ручная правка пользователя
// приоритетнее ответа модели и не перезатирается.
func (g *GroupStore) UpsertWord(ctx context.Context, userID int64, word string,
	categoryID int32, memberID *int64, source string) error {
	_, err := g.pool.Exec(ctx, `
		insert into word_map (group_id, user_id, word, category_id, member_id, source)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (group_id, user_id, word) do update
		set category_id = case when word_map.source = 'manual' and excluded.source <> 'manual'
		                       then word_map.category_id else excluded.category_id end,
		    member_id   = case when word_map.source = 'manual' and excluded.source <> 'manual'
		                       then word_map.member_id else excluded.member_id end,
		    source      = case when word_map.source = 'manual' and excluded.source <> 'manual'
		                       then word_map.source else excluded.source end,
		    hits        = word_map.hits + 1,
		    updated_at  = now()`,
		g.groupID, userID, word, categoryID, memberID, source)
	return err
}

// ForgetLLMWords выбрасывает из личных словарей группы слова, привязанные
// моделью.
//
// Список категорий изменился — значит прежние догадки устарели: «пиво»,
// однажды разобранное в Продукты, иначе резолвилось бы туда вечно, даже
// после появления категории «Алкоголь». Ручные привязки не трогаем.
func (g *GroupStore) ForgetLLMWords(ctx context.Context) (int64, error) {
	tag, err := g.pool.Exec(ctx,
		`delete from word_map where group_id = $1 and source = 'llm'`, g.groupID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MaxCategoryNameLen — потолок длины названия категории.
const MaxCategoryNameLen = 64

// CreateCategory заводит новую категорию группы в конец списка.
//
// template_key у неё пуст: слов в общей затравке для неё нет и быть не может,
// а выдуманный ключ засорял бы пространство имён, общее для всех групп.
func (g *GroupStore) CreateCategory(ctx context.Context, name, hint string, memberID *int64) (Category, error) {
	c := Category{Name: trimTo(name, MaxCategoryNameLen), Hint: hint, DefaultMemberID: memberID}
	err := g.pool.QueryRow(ctx, `
		insert into categories (group_id, name, hint, default_member_id, sort_order)
		values ($1, $2, $3, $4,
		        (select coalesce(max(sort_order), 0) + 10 from categories where group_id = $1))
		returning id, sort_order`, g.groupID, c.Name, hint, memberID).
		Scan(&c.ID, &c.SortOrder)
	return c, err
}

// UpdateCategory правит имя, подсказку и адресата по умолчанию.
func (g *GroupStore) UpdateCategory(ctx context.Context, id int32, name, hint string, memberID *int64) (bool, error) {
	tag, err := g.pool.Exec(ctx, `
		update categories set name = $3, hint = $4, default_member_id = $5
		where id = $2 and group_id = $1`,
		g.groupID, id, trimTo(name, MaxCategoryNameLen), hint, memberID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
