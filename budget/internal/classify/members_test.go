package classify

import (
	"testing"

	"budget/internal/storage"
)

func TestRosterDisambiguatesEqualNames(t *testing.T) {
	// Имя принадлежит человеку, а не группе, и внутри группы может
	// повториться. Модели нужен однозначный перечень: иначе она возвращает
	// «Аня», а мы не знаем, которую из двух.
	r := NewRoster([]storage.Member{
		{ID: 11, Name: "Илья"},
		{ID: 12, Name: "Аня"},
		{ID: 13, Name: "Аня"},
		{ID: 14, Name: "Аня"},
	})

	labels := r.Labels()
	want := []string{"Илья", "Аня", "Аня (2)", "Аня (3)"}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("подписи = %v, ожидались %v", labels, want)
		}
	}

	if id, ok := r.Member("Аня"); !ok || id != 12 {
		t.Errorf("«Аня» = %d (%v), ожидалась первая (12)", id, ok)
	}
	if id, ok := r.Member("Аня (3)"); !ok || id != 14 {
		t.Errorf("«Аня (3)» = %d (%v), ожидалась третья (14)", id, ok)
	}
}

func TestRosterIgnoresCase(t *testing.T) {
	// Модель охотно отвечает «аня» вместо «Аня» — терять из-за этого
	// получателя незачем.
	r := NewRoster([]storage.Member{{ID: 12, Name: "Аня"}})

	if id, ok := r.Member("аня"); !ok || id != 12 {
		t.Errorf("«аня» = %d (%v), ожидалась Аня (12)", id, ok)
	}
	if id, ok := r.Member("  Аня  "); !ok || id != 12 {
		t.Errorf("имя в пробелах = %d (%v), ожидалась Аня (12)", id, ok)
	}
	if _, ok := r.Member("Дима"); ok {
		t.Error("чужого в группе быть не должно")
	}
}

func TestRosterEnumEndsWithEveryone(t *testing.T) {
	r := NewRoster([]storage.Member{{ID: 11, Name: "Илья"}, {ID: 12, Name: "Аня"}})

	enum := r.Enum()
	if len(enum) != 3 || enum[2] != Everyone {
		t.Errorf("перечисление = %v, последним ожидалось «%s»", enum, Everyone)
	}
	// Enum не должен портить список подписей: он строится копией.
	if len(r.Labels()) != 2 {
		t.Errorf("подписи после Enum = %v, ожидались двое", r.Labels())
	}
}

func TestRosterNamesMemberCalledEveryone(t *testing.T) {
	// Участник, названный служебной фразой, реальнее выдуманного нами
	// значения: Member ищется раньше, чем IsEveryone.
	r := NewRoster([]storage.Member{{ID: 11, Name: "Илья"}, {ID: 12, Name: Everyone}})

	if id, ok := r.Member(Everyone); !ok || id != 12 {
		t.Errorf("участник с таким именем = %d (%v), ожидался 12", id, ok)
	}
}

func TestRememberedRecipientOnlyWhenSingle(t *testing.T) {
	// Словарь помнит получателя, только если он один: для списка в word_map
	// нет колонки, а nil означает «взять умолчание категории».
	if got := RememberedRecipient(Item{Recipients: []int64{12}}); got == nil || *got != 12 {
		t.Errorf("один получатель = %v, ожидался 12", got)
	}
	if got := RememberedRecipient(Item{Recipients: []int64{12, 13}}); got != nil {
		t.Errorf("двое получателей = %v, ожидался nil", *got)
	}
	if got := RememberedRecipient(Item{}); got != nil {
		t.Errorf("общая трата = %v, ожидался nil", *got)
	}
}
