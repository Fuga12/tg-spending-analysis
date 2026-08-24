package classify

import (
	"testing"

	"budget/internal/storage"
)

// catsWithDefault — те же категории, но у Продуктов задано умолчание.
func catsWithDefault(d storage.CategoryDefault) []storage.Category {
	cats := testCategories()
	cats[0].Default = d
	return cats
}

// parse прогоняет ответ модели без указанного получателя: именно этот случай
// разрешается умолчанием категории.
func parse(t *testing.T, cats []storage.Category) Item {
	t.Helper()
	items := Validate([]RawItem{raw("3200", "продукты", "Продукты", KindExpense, 0)},
		testRequest("продукты 3200", cats), quietLog())
	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	return items[0]
}

func TestDefaultCommonMakesSpendingGroupWide(t *testing.T) {
	// Ради этого всё и затевалось: «продукты 3200» без единого слова о
	// получателе — общая трата, а не трата того, кто в этот раз расплатился.
	item := parse(t, catsWithDefault(storage.CategoryDefault{Common: true}))

	if len(item.Recipients) != 0 {
		t.Fatalf("категория с умолчанием «на всю группу» должна давать общую трату, получено %v",
			item.Recipients)
	}
}

func TestDefaultCommonOutranksPayer(t *testing.T) {
	// Плательщик известен и состоит в группе — и всё равно не должен попасть в
	// получатели: иначе общая трата разложилась бы на одного человека, а в
	// отчёте «Общее» осталось бы пустым.
	cats := catsWithDefault(storage.CategoryDefault{Common: true})
	req := testRequest("продукты 3200", cats)
	if req.Payer.ID == 0 {
		t.Fatal("тест бессмыслен без плательщика")
	}

	got := defaultRecipients(req, &cats[0])
	if len(got) != 0 {
		t.Fatalf("умолчание «на всю группу» перебивает плательщика, получено %v", got)
	}
}

func TestDefaultMemberStillWins(t *testing.T) {
	// Прежнее поведение не должно было съехать: адресат-человек работает
	// ровно как работал.
	anya := int64(12)
	item := parse(t, catsWithDefault(storage.CategoryDefault{MemberID: &anya}))

	if len(item.Recipients) != 1 || item.Recipients[0] != anya {
		t.Fatalf("ожидался адресат категории %d, получено %v", anya, item.Recipients)
	}
}

func TestNoDefaultStillMeansPayer(t *testing.T) {
	item := parse(t, testCategories())

	if len(item.Recipients) != 1 || item.Recipients[0] != testMembers()[0].ID {
		t.Fatalf("без умолчания трата остаётся за плательщиком, получено %v", item.Recipients)
	}
}

func TestStatedRecipientOutranksCommonDefault(t *testing.T) {
	// «Продукты Ане 3200» при категории «на всю группу»: сказанное вслух
	// сильнее настройки, иначе настройку нельзя было бы обойти ни одним
	// сообщением.
	cats := catsWithDefault(storage.CategoryDefault{Common: true})
	r := raw("3200", "продукты", "Продукты", KindExpense, 0)
	r.Recipients = []string{"Аня"}
	r.RecipientsStated = true

	items := Validate([]RawItem{r}, testRequest("продукты Ане 3200", cats), quietLog())
	if len(items) != 1 {
		t.Fatalf("ожидался один элемент, получено %d", len(items))
	}
	if len(items[0].Recipients) != 1 || items[0].Recipients[0] != 12 {
		t.Fatalf("названный получатель должен перебить умолчание, получено %v", items[0].Recipients)
	}
}
