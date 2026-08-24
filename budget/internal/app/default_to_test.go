package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"budget/internal/storage"
)

// Кодек default_to проверяется отдельно от сервера: он единственное место, где
// три состояния ужимаются в одно поле JSON, и ошибка здесь молча превращает
// «на всю группу» в «на плательщика» — то есть портит данные, а не отвечает
// ошибкой.
func TestDefaultToRoundTrip(t *testing.T) {
	id := int64(42)
	cases := []struct {
		name string
		val  defaultTo
		json string
	}{
		{"плательщик", defaultTo{}, "null"},
		{"вся группа", defaultTo{Common: true}, `"common"`},
		{"участник", defaultTo{MemberID: &id}, "42"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.val)
			if err != nil {
				t.Fatalf("сериализация: %v", err)
			}
			if string(b) != c.json {
				t.Fatalf("сериализация = %s, ожидалось %s", b, c.json)
			}

			var back defaultTo
			if err := json.Unmarshal([]byte(c.json), &back); err != nil {
				t.Fatalf("разбор: %v", err)
			}
			if back.Common != c.val.Common {
				t.Fatalf("общая = %v, ожидалось %v", back.Common, c.val.Common)
			}
			switch {
			case (back.MemberID == nil) != (c.val.MemberID == nil):
				t.Fatalf("участник = %v, ожидалось %v", back.MemberID, c.val.MemberID)
			case back.MemberID != nil && *back.MemberID != *c.val.MemberID:
				t.Fatalf("участник = %d, ожидалось %d", *back.MemberID, *c.val.MemberID)
			}
		})
	}
}

func TestDefaultToRejectsNonsense(t *testing.T) {
	// Незнакомое значение обязано быть ошибкой, а не молчаливым null: иначе
	// опечатка в клиенте разложила бы общие траты по плательщикам, и заметили
	// бы это через месяц по отчёту.
	for _, bad := range []string{`"всем"`, `true`, `{}`, `[12]`, `"42"`} {
		var d defaultTo
		if err := json.Unmarshal([]byte(bad), &d); err == nil {
			t.Errorf("%s разобралось как %+v, ожидалась ошибка", bad, d)
		}
	}
}

func TestCategoryDefaultCommonSurvivesRoundTrip(t *testing.T) {
	// Сквозь весь слой: записали через API — прочитали из базы — увидели в
	// /api/state. Именно эта цепочка ломалась бы при расхождении имён колонок.
	ts := newTestServer(t)
	ctx := context.Background()

	// Имя нарочно не из затравки: «Коммуналка» там уже есть, и тест падал бы
	// на конфликте имён, делая вид, что дело в умолчании.
	w := ts.do(t, 1, "POST", "/api/categories",
		`{"name":"Ремонт","hint":"плитка, краска","default_to":"common"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("создание категории: код = %d, %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"default_to":"common"`) {
		t.Fatalf("ответ на создание = %s, ожидалось default_to=common", w.Body.String())
	}

	cats, err := ts.group.Categories(ctx)
	if err != nil {
		t.Fatalf("чтение категорий: %v", err)
	}
	var created *storage.Category
	for i := range cats {
		if cats[i].Name == "Ремонт" {
			created = &cats[i]
		}
	}
	if created == nil {
		t.Fatal("категория не появилась в базе")
	}
	if !created.Default.Common || created.Default.MemberID != nil {
		t.Fatalf("умолчание в базе = %+v, ожидалось только Common", created.Default)
	}

	if w := ts.do(t, 1, "GET", "/api/state", ""); !strings.Contains(w.Body.String(), `"default_to":"common"`) {
		t.Fatalf("в /api/state умолчание не доехало: %s", w.Body.String())
	}
}

func TestCategoryDefaultCommonClearsMember(t *testing.T) {
	// Два поля, один выбор: переключение на «всю группу» обязано стереть
	// адресата, иначе в базе осталась бы пара, запрещённая check-ом.
	ts := newTestServer(t)
	ctx := context.Background()

	members, err := ts.group.Members(ctx)
	if err != nil {
		t.Fatalf("состав группы: %v", err)
	}
	me := members[0].ID

	cat, err := ts.group.CreateCategory(ctx, "Косметика", "",
		storage.CategoryDefault{MemberID: &me})
	if err != nil {
		t.Fatalf("создание категории: %v", err)
	}

	if _, err := ts.group.UpdateCategory(ctx, cat.ID, "Косметика", "",
		storage.CategoryDefault{MemberID: &me, Common: true}); err != nil {
		t.Fatalf("правка категории: %v", err)
	}

	cats, _ := ts.group.Categories(ctx)
	for _, c := range cats {
		if c.Name != "Косметика" {
			continue
		}
		if !c.Default.Common || c.Default.MemberID != nil {
			t.Fatalf("умолчание = %+v, ожидалось только Common", c.Default)
		}
		return
	}
	t.Fatal("категория пропала")
}

func TestDuplicateCategoryNameIsNotADatabaseOutage(t *testing.T) {
	// «Коммуналка» приходит из затравки. Ответ обязан объяснять, что не так:
	// прежде сюда доезжало «База не отвечает, попробуй ещё раз» — совет,
	// который не сработает ни на какой попытке.
	ts := newTestServer(t)

	w := ts.do(t, 1, "POST", "/api/categories", `{"name":"Коммуналка","default_to":null}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("код = %d, ожидался 409; тело: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "уже есть") {
		t.Fatalf("ответ = %s, ожидалось объяснение про занятое название", w.Body.String())
	}
}
