package classify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"budget/internal/storage"
)

// Корпус — полсотни настоящих сообщений с ожидаемым разбором.
//
// Модель здесь фейковая: проверяется не она, а всё вокруг неё — извлечение
// сумм, выбор быстрого пути, разрешение получателей, умолчания категорий,
// деградация. Без такой сетки любая правка промпта или валидации — ставка
// вслепую: чинишь «пиво», ломаешь «вчера пятёрочка 1200 и такси 400».
//
// Случаи лежат данными, а не кодом, чтобы добавить новый стоило пять строк,
// и чтобы тот же файл можно было однажды прогнать против живой модели.

type corpusCase struct {
	Text string `json:"text"`
	Note string `json:"note"`
	// Source — каким путём разбор обязан пойти: llm (по умолчанию), cache
	// или degraded.
	Source string `json:"source"`
	// Error — ожидаемый отказ вместо разбора. Пока только no_amount.
	Error string `json:"error"`
	// Dict — что лежит в словаре до разбора.
	Dict map[string]corpusWord `json:"dict"`
	// Model — что вернула бы модель. Пусто, если до неё дело не доходит.
	Model []RawItem    `json:"model"`
	Want  []corpusItem `json:"want"`
}

type corpusWord struct {
	Category string `json:"category"`
	Source   string `json:"source"`
	Hits     int    `json:"hits"`
	Member   string `json:"member"`
}

type corpusItem struct {
	Amount      string   `json:"amount"`
	Description string   `json:"description"`
	Category    string   `json:"category"` // пусто — без категории
	Recipients  []string `json:"recipients"`
	Kind        string   `json:"kind"`
	DaysAgo     int      `json:"days_ago"`
	Pending     bool     `json:"pending"` // категорию доберёт воркер
}

// Группа из троих. Имена в корпусе — эти же.
var corpusMembers = []storage.Member{
	{ID: 11, GroupID: 1, UserID: 101, Name: "Илья", Role: storage.RoleAdmin},
	{ID: 12, GroupID: 1, UserID: 102, Name: "Аня", Role: storage.RoleMember},
	{ID: 13, GroupID: 1, UserID: 103, Name: "Уля", Role: storage.RoleMember},
}

// corpusCategories повторяет шаблон из миграции: имена и ключи должны быть
// теми же, иначе корпус проверяет несуществующую группу.
var corpusCategories = []storage.Category{
	{ID: 1, Name: "Продукты", TemplateKey: "groceries", SortOrder: 10},
	{ID: 2, Name: "Кафе и рестораны", TemplateKey: "cafe", SortOrder: 20},
	{ID: 3, Name: "Доставка еды", TemplateKey: "delivery", SortOrder: 30},
	{ID: 4, Name: "Транспорт", TemplateKey: "transport", SortOrder: 40},
	{ID: 5, Name: "Такси", TemplateKey: "taxi", SortOrder: 50},
	{ID: 6, Name: "Жильё", TemplateKey: "housing", SortOrder: 60},
	{ID: 7, Name: "Коммуналка", TemplateKey: "utilities", SortOrder: 70},
	{ID: 8, Name: "Связь и интернет", TemplateKey: "telecom", SortOrder: 80},
	{ID: 9, Name: "Здоровье", TemplateKey: "health", SortOrder: 90},
	{ID: 10, Name: "Одежда", TemplateKey: "clothes", SortOrder: 100},
	{ID: 11, Name: "Хобби и развлечения", TemplateKey: "hobby", SortOrder: 110},
	{ID: 12, Name: "Подарки", TemplateKey: "gifts", SortOrder: 120},
	{ID: 13, Name: "Дом и быт", TemplateKey: "home", SortOrder: 130},
	{ID: 14, Name: "Прочее", TemplateKey: storage.TemplateOther, SortOrder: 140},
}

// corpusDict — словарь и состав группы в памяти.
type corpusDict struct {
	words map[string]storage.WordHit
}

func (d *corpusDict) Categories(context.Context) ([]storage.Category, error) {
	return corpusCategories, nil
}

func (d *corpusDict) Members(context.Context) ([]storage.Member, error) {
	return corpusMembers, nil
}

func (d *corpusDict) LookupWords(_ context.Context, _ int64, words []string) (map[string]storage.WordHit, error) {
	out := map[string]storage.WordHit{}
	for _, w := range words {
		if h, ok := d.words[w]; ok {
			h.Word = w
			out[w] = h
		}
	}
	return out, nil
}

// corpusLLM отдаёт заранее записанный ответ модели и считает обращения.
type corpusLLM struct {
	calls int
	items []RawItem
}

func (l *corpusLLM) Parse(_ context.Context, _ UsageRecorder, _ Request) ([]RawItem, error) {
	l.calls++
	if l.items == nil {
		return nil, &Error{Kind: storage.ErrKindSchema, Err: errors.New("модель вернула пустой ответ")}
	}
	return l.items, nil
}

func TestCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatalf("корпус: %v", err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("разбор корпуса: %v", err)
	}
	if len(cases) < 50 {
		t.Fatalf("в корпусе %d случаев, договаривались про полсотни", len(cases))
	}

	for _, c := range cases {
		t.Run(c.Text+" — "+c.Note, func(t *testing.T) {
			llm := &corpusLLM{items: c.Model}
			svc := NewService(llm, openGate{}, alwaysAllowBudget{}, quietLog())
			sc := Scope{
				Payer: corpusMembers[0],
				Dict:  &corpusDict{words: corpusWords(t, c.Dict)},
				Usage: noUsage{},
			}

			res, err := svc.Classify(context.Background(), sc, c.Text)

			if c.Error != "" {
				if c.Error != "no_amount" {
					t.Fatalf("неизвестный ожидаемый отказ %q", c.Error)
				}
				if !errors.Is(err, ErrNoAmount) {
					t.Fatalf("ошибка = %v, ожидалась ErrNoAmount", err)
				}
				if llm.calls != 0 {
					t.Errorf("сетевых вызовов %d, без суммы в модель ходить незачем", llm.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}

			wantSource := Source(c.Source)
			if wantSource == "" {
				wantSource = SourceLLM
			}
			if res.Source != wantSource {
				t.Errorf("источник = %q, ожидался %q", res.Source, wantSource)
			}
			if wantSource == SourceCache && llm.calls != 0 {
				t.Errorf("сетевых вызовов %d, быстрый путь ходить в сеть не должен", llm.calls)
			}

			assertItems(t, res.Items, c.Want)
		})
	}
}

func assertItems(t *testing.T, got []Item, want []corpusItem) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("элементов %d, ожидалось %d (получено: %s)", len(got), len(want), showItems(got))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Amount.String() != w.Amount {
			t.Errorf("[%d] сумма = %s, ожидалась %s", i, g.Amount, w.Amount)
		}
		if g.Description != w.Description {
			t.Errorf("[%d] описание = %q, ожидалось %q", i, g.Description, w.Description)
		}
		if name := categoryNameByID(g.CategoryID); name != w.Category {
			t.Errorf("[%d] категория = %q, ожидалась %q", i, name, w.Category)
		}
		if g.Kind != w.Kind {
			t.Errorf("[%d] вид = %q, ожидался %q", i, g.Kind, w.Kind)
		}
		if g.DaysAgo != w.DaysAgo {
			t.Errorf("[%d] days_ago = %d, ожидалось %d", i, g.DaysAgo, w.DaysAgo)
		}
		if g.NeedsClassification != w.Pending {
			t.Errorf("[%d] «разобрать позже» = %v, ожидалось %v", i, g.NeedsClassification, w.Pending)
		}
		if names := memberNames(g.Recipients); !equalStrings(names, w.Recipients) {
			t.Errorf("[%d] получатели = %v, ожидались %v", i, names, w.Recipients)
		}
	}
}

// corpusWords переводит словарь корпуса в то, что отдаёт хранилище.
func corpusWords(t *testing.T, in map[string]corpusWord) map[string]storage.WordHit {
	t.Helper()
	out := make(map[string]storage.WordHit, len(in))
	for word, w := range in {
		hit := storage.WordHit{
			Word:       word,
			CategoryID: corpusCategoryID(t, w.Category),
			Source:     w.Source,
			Hits:       w.Hits,
		}
		if w.Member != "" {
			hit.MemberID = corpusMemberID(t, w.Member)
		}
		out[word] = hit
	}
	return out
}

func corpusCategoryID(t *testing.T, name string) int32 {
	t.Helper()
	for _, c := range corpusCategories {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("в корпусе категория %q, которой нет в шаблоне", name)
	return 0
}

func corpusMemberID(t *testing.T, name string) *int64 {
	t.Helper()
	for _, m := range corpusMembers {
		if m.Name == name {
			id := m.ID
			return &id
		}
	}
	t.Fatalf("в корпусе участник %q, которого нет в группе", name)
	return nil
}

func categoryNameByID(id *int32) string {
	if id == nil {
		return ""
	}
	for _, c := range corpusCategories {
		if c.ID == *id {
			return c.Name
		}
	}
	return "неизвестная категория"
}

func memberNames(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		name := "неизвестный участник"
		for _, m := range corpusMembers {
			if m.ID == id {
				name = m.Name
			}
		}
		out = append(out, name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func showItems(items []Item) string {
	parts := make([]string, 0, len(items))
	for _, i := range items {
		parts = append(parts, i.Amount.String()+" "+i.Description+" ["+i.Kind+"]")
	}
	return strings.Join(parts, "; ")
}
