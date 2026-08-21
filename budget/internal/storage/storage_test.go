package storage

import (
	"context"
	"os"
	"testing"
	"time"
)

// testStore поднимает хранилище на настоящей базе. Без TEST_DATABASE_URL
// тесты пропускаются: SQL нельзя проверить фейком, а гонять его вслепую —
// значит не проверять вовсе.
func testStore(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан — интеграционные тесты пропущены")
	}
	// База одна на все пакеты: гонять их параллельно нельзя, они чистят
	// таблицы друг у друга. Запускать через make test-db (там -p 1).

	ctx := context.Background()
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	s, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(s.Close)

	// Шаблон категорий и затравка словаря остаются: они часть схемы, а не
	// данные группы.
	if _, err := s.pool.Exec(ctx, `
		truncate transactions, tx_recipients, word_map, llm_usage,
		         categories, invites, members, groups, users
		restart identity cascade`); err != nil {
		t.Fatalf("очистка: %v", err)
	}
	return s
}

// testGroup заводит группу из n человек с telegram id 1..n. Первый —
// администратор. Возвращает скоупнутое хранилище и участников по порядку.
func testGroup(t *testing.T, s *Store, n int) (*GroupStore, []Member) {
	t.Helper()
	ctx := context.Background()

	names := []string{"Илья", "Аня", "Уля", "Дима", "Мила", "Кир", "Ася", "Лев", "Рома", "Ника"}
	for i := 1; i <= n; i++ {
		if err := s.EnsureUser(ctx, int64(i), names[(i-1)%len(names)]); err != nil {
			t.Fatalf("пользователь %d: %v", i, err)
		}
	}

	g, admin, err := s.CreateGroup(ctx, "Тест", 1)
	if err != nil {
		t.Fatalf("создание группы: %v", err)
	}
	gs := s.ForGroup(g.ID)

	members := []Member{admin}
	for i := 2; i <= n; i++ {
		m, err := gs.AddMember(ctx, int64(i), RoleMember)
		if err != nil {
			t.Fatalf("участник %d: %v", i, err)
		}
		members = append(members, m)
	}
	return gs, members
}

func TestEnsureUserDoesNotOverwriteChosenName(t *testing.T) {
	// EnsureUser зовётся на каждое сообщение и подставляет имя из профиля
	// Telegram. Имя, выбранное человеком в приложении, оно затирать не имеет
	// права: Telegram зовёт «Ульяночка», в бюджете она Уля.
	s := testStore(t)
	ctx := context.Background()

	if err := s.EnsureUser(ctx, 1, "Ульяночка"); err != nil {
		t.Fatalf("заведение: %v", err)
	}
	if err := s.SetName(ctx, 1, "Уля"); err != nil {
		t.Fatalf("переименование: %v", err)
	}
	if err := s.EnsureUser(ctx, 1, "Ульяночка"); err != nil {
		t.Fatalf("повторное сообщение: %v", err)
	}

	if _, _, err := s.CreateGroup(ctx, "Тест", 1); err != nil {
		t.Fatalf("группа: %v", err)
	}
	m, ok, err := s.MemberOf(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("поиск участника: ok=%v err=%v", ok, err)
	}
	if m.Name != "Уля" {
		t.Errorf("имя = %q, ожидалось выбранное человеком «Уля»", m.Name)
	}
}

func TestCreateGroupRollsOutCategoryTemplate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 1)

	cats, err := g.Categories(ctx)
	if err != nil {
		t.Fatalf("категории: %v", err)
	}
	if len(cats) != 14 {
		t.Fatalf("категорий %d, в шаблоне 14", len(cats))
	}
	if cats[0].Name != "Продукты" || cats[len(cats)-1].Name != "Прочее" {
		t.Errorf("порядок категорий = %s ... %s", cats[0].Name, cats[len(cats)-1].Name)
	}
	// Категория-свалка ищется по ключу шаблона, а не по имени: её могут
	// переименовать.
	if cats[len(cats)-1].TemplateKey != TemplateOther {
		t.Errorf("ключ последней категории = %q, ожидался %q",
			cats[len(cats)-1].TemplateKey, TemplateOther)
	}

	// Категории второй группы — свои, а не общие с первой.
	if err := s.EnsureUser(ctx, 99, "Чужой"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	other, _, err := s.CreateGroup(ctx, "Другая", 99)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}
	otherCats, _ := s.ForGroup(other.ID).Categories(ctx)
	if len(otherCats) != 14 {
		t.Fatalf("категорий второй группы %d, ожидалось 14", len(otherCats))
	}
	if otherCats[0].ID == cats[0].ID {
		t.Error("категории двух групп не должны быть одной и той же строкой")
	}
}

func TestMemberOfIsEmptyUntilGroupExists(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.EnsureUser(ctx, 1, "Илья"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	// Человек боту известен, но в группе не состоит — записывать некуда,
	// и это нормальное состояние только что пришедшего.
	if _, ok, err := s.MemberOf(ctx, 1); err != nil || ok {
		t.Errorf("без группы MemberOf = ok:%v err:%v, ожидалось ok:false", ok, err)
	}
}

func TestOnePersonOneGroup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _ = testGroup(t, s, 2)

	if err := s.EnsureUser(ctx, 50, "Гость"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	first, _, err := s.CreateGroup(ctx, "Первая", 50)
	if err != nil {
		t.Fatalf("первая группа: %v", err)
	}
	second, _, err := s.CreateGroup(ctx, "Вторая", 1)
	if err == nil {
		t.Fatalf("человек из группы %d завёл ещё одну (%d) — это запрещено",
			first.ID, second.ID)
	}
}

func TestSeedWordsLandOnGroupCategories(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 2)

	cats, _ := g.Categories(ctx)
	food := categoryID(t, cats, "Продукты")
	taxi := categoryID(t, cats, "Такси")

	hits, err := g.LookupWords(ctx, 1, []string{"пятёрочка", "такси", "неизвестноеслово"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("найдено %d слов (%+v), ожидалось два", len(hits), hits)
	}
	// Затравка общая на всех и хранит ключ шаблона — приземлиться она обязана
	// на категории именно этой группы.
	if h := hits["пятёрочка"]; h.CategoryID != food || h.Source != SourceSeed {
		t.Errorf("«пятёрочка» = %+v, ожидалась затравка в Продукты (%d)", h, food)
	}
	if h := hits["такси"]; h.CategoryID != taxi {
		t.Errorf("«такси» = %+v, ожидалась категория Такси (%d)", h, taxi)
	}
	if _, ok := hits["неизвестноеслово"]; ok {
		t.Error("незнакомое слово не должно находиться")
	}
}

func TestLookupWordsPrefersPersonalCache(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 2)

	cats, _ := g.Categories(ctx)
	taxi := categoryID(t, cats, "Такси")

	// «самокат» в затравке — Продукты. Пользователь сказал: это Такси.
	if err := g.UpsertWord(ctx, 1, "самокат", taxi, nil, SourceManual); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hits, err := g.LookupWords(ctx, 1, []string{"самокат"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if h := hits["самокат"]; h.Source != SourceManual || h.CategoryID != taxi {
		t.Errorf("«самокат» = %+v, ожидалась ручная привязка к Такси", h)
	}

	// Словарь личный: второму участнику той же группы он не виден.
	hits, _ = g.LookupWords(ctx, 2, []string{"самокат"})
	if h := hits["самокат"]; h.Source != SourceSeed {
		t.Errorf("у второго участника «самокат» = %+v, ожидалась затравка", h)
	}
}

func TestDictionaryIsScopedToGroup(t *testing.T) {
	// Худший баг мультитенантности — увидеть чужое. Словарь одного человека
	// в двух группах разный: категории у групп свои, и привязка к чужой
	// категории бессмысленна.
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 1)

	cats, _ := g.Categories(ctx)
	taxi := categoryID(t, cats, "Такси")
	if err := g.UpsertWord(ctx, 1, "самокат", taxi, nil, SourceManual); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.EnsureUser(ctx, 99, "Чужой"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	other, _, err := s.CreateGroup(ctx, "Другая", 99)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}

	hits, err := s.ForGroup(other.ID).LookupWords(ctx, 1, []string{"самокат"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if h := hits["самокат"]; h.Source == SourceManual {
		t.Errorf("в чужой группе «самокат» = %+v — личная привязка не должна протекать", h)
	}
}

func TestUpsertWordDoesNotOverwriteManual(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 1)

	cats, _ := g.Categories(ctx)
	taxi := categoryID(t, cats, "Такси")
	food := categoryID(t, cats, "Продукты")

	if err := g.UpsertWord(ctx, 1, "самокат", taxi, nil, SourceManual); err != nil {
		t.Fatalf("ручная привязка: %v", err)
	}
	// Модель считает иначе — и не должна перебить пользователя (§8).
	if err := g.UpsertWord(ctx, 1, "самокат", food, nil, SourceLLM); err != nil {
		t.Fatalf("привязка моделью: %v", err)
	}

	hits, _ := g.LookupWords(ctx, 1, []string{"самокат"})
	h := hits["самокат"]
	if h.CategoryID != taxi || h.Source != SourceManual {
		t.Errorf("после ответа модели = %+v, ожидалась сохранённая ручная привязка", h)
	}
	if h.Hits != 2 {
		t.Errorf("hits = %d, ожидалось 2 — счётчик растёт в любом случае", h.Hits)
	}

	// А вот новая ручная правка перебивает старую.
	if err := g.UpsertWord(ctx, 1, "самокат", food, nil, SourceManual); err != nil {
		t.Fatalf("вторая ручная правка: %v", err)
	}
	hits, _ = g.LookupWords(ctx, 1, []string{"самокат"})
	if hits["самокат"].CategoryID != food {
		t.Errorf("категория = %d, ожидалась новая ручная (%d)", hits["самокат"].CategoryID, food)
	}
}

func TestForgetLLMWordsKeepsManualAndOtherGroups(t *testing.T) {
	// Список категорий изменился — догадки модели устарели: «пиво», однажды
	// разобранное в Продукты, иначе резолвилось бы туда и после появления
	// категории «Алкоголь». Ручные привязки при этом трогать нельзя, а чужие
	// группы не касается вовсе.
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 1)

	cats, _ := g.Categories(ctx)
	food := categoryID(t, cats, "Продукты")
	taxi := categoryID(t, cats, "Такси")

	if err := g.UpsertWord(ctx, 1, "пиво", food, nil, SourceLLM); err != nil {
		t.Fatalf("привязка моделью: %v", err)
	}
	if err := g.UpsertWord(ctx, 1, "самокат", taxi, nil, SourceManual); err != nil {
		t.Fatalf("ручная привязка: %v", err)
	}

	if err := s.EnsureUser(ctx, 99, "Чужой"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	otherGroup, _, err := s.CreateGroup(ctx, "Другая", 99)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}
	og := s.ForGroup(otherGroup.ID)
	otherCats, _ := og.Categories(ctx)
	if err := og.UpsertWord(ctx, 99, "пиво", categoryID(t, otherCats, "Продукты"), nil, SourceLLM); err != nil {
		t.Fatalf("привязка в чужой группе: %v", err)
	}

	n, err := g.ForgetLLMWords(ctx)
	if err != nil {
		t.Fatalf("сброс словаря: %v", err)
	}
	if n != 1 {
		t.Errorf("удалено слов: %d, ожидалось 1", n)
	}

	hits, _ := g.LookupWords(ctx, 1, []string{"пиво", "самокат"})
	if h, ok := hits["пиво"]; ok && h.Source == SourceLLM {
		t.Error("догадка модели должна была уйти из личного словаря")
	}
	if hits["самокат"].Source != SourceManual {
		t.Errorf("ручная привязка = %+v, её сброс не касается", hits["самокат"])
	}

	otherHits, _ := og.LookupWords(ctx, 99, []string{"пиво"})
	if otherHits["пиво"].Source != SourceLLM {
		t.Errorf("в чужой группе «пиво» = %+v — сброс не должен её касаться", otherHits["пиво"])
	}
}

func TestMonthlyUsageIgnoresPreviousMonth(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 1)

	if err := g.RecordUsage(ctx, "gpt://f/m", 650, 50, true, ""); err != nil {
		t.Fatalf("запись расхода: %v", err)
	}
	if err := g.RecordUsage(ctx, "gpt://f/m", 0, 0, false, ErrKindQuota); err != nil {
		t.Fatalf("запись расхода: %v", err)
	}
	// Строка из прошлого месяца в текущий счётчик попадать не должна (§11).
	_, err := s.pool.Exec(ctx, `
		insert into llm_usage (created_at, model, prompt_tokens, completion_tokens, ok)
		values (date_trunc('month', now()) - interval '3 days', 'gpt://f/m', 1000000, 1000, true)`)
	if err != nil {
		t.Fatalf("прошлый месяц: %v", err)
	}

	used, err := s.MonthlyUsage(ctx)
	if err != nil {
		t.Fatalf("расход: %v", err)
	}
	if used.TotalTokens != 700 {
		t.Errorf("токенов за месяц %d, ожидалось 700 — прошлый месяц не считается", used.TotalTokens)
	}
	if used.Calls != 2 || used.Failed != 1 {
		t.Errorf("вызовов %d, неуспешных %d, ожидалось 2 и 1", used.Calls, used.Failed)
	}

	// Группа проставляется сразу, хотя тарификация групп будет позже.
	var withGroup int
	if err := s.pool.QueryRow(ctx,
		`select count(*) from llm_usage where group_id = $1`, g.GroupID()).Scan(&withGroup); err != nil {
		t.Fatalf("проверка группы: %v", err)
	}
	if withGroup != 2 {
		t.Errorf("строк с группой %d, ожидалось 2", withGroup)
	}
}

func TestRecordUsageKeepsErrorKind(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 1)

	for _, kind := range []string{ErrKindTimeout, ErrKindQuota, ErrKindHTTP, ErrKindSchema, ErrKindOther} {
		if err := g.RecordUsage(ctx, "gpt://f/m", 0, 0, false, kind); err != nil {
			t.Fatalf("вид ошибки %q не записался: %v", kind, err)
		}
	}
	var stored int
	if err := s.pool.QueryRow(ctx,
		`select count(distinct error_kind) from llm_usage where created_at > $1`,
		time.Now().Add(-time.Minute)).Scan(&stored); err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if stored != 5 {
		t.Errorf("различных видов ошибок %d, ожидалось 5", stored)
	}
}

func categoryID(t *testing.T, cats []Category, name string) int32 {
	t.Helper()
	for _, c := range cats {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("категория %q не найдена", name)
	return 0
}

// memberIDs — короткая запись списка получателей в тестах.
func memberIDs(members []Member, idx ...int) []int64 {
	out := make([]int64, 0, len(idx))
	for _, i := range idx {
		out = append(out, members[i].ID)
	}
	return out
}
