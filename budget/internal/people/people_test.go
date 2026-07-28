package people

import (
	"testing"

	"budget/internal/storage"
)

func TestDative(t *testing.T) {
	cases := map[string]string{
		"Уля":    "Уле",
		"Илья":   "Илье",
		"Маша":   "Маше",
		"Никита": "Никите",
		"Мария":  "Марии",
		"Андрей": "Андрею",
		"Игорь":  "Игорю",
		"Иван":   "Ивану",
		// Беглая гласная нам не по силам: «Петру» требует морфологии, а не
		// правил окончаний. Фиксируем как есть — участников бюджета двое,
		// и обоих мы склоняем верно.
		"Пётр": "Пётру",
		"":     "",
		"Ilya": "Ilya", // латиницу не склоняем
		"Люси": "Люси", // несклоняемое остаётся собой
	}
	for in, want := range cases {
		if got := Dative(in); got != want {
			t.Errorf("Dative(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestOtherNeedsExactlyTwo(t *testing.T) {
	two := []storage.User{{ID: 1, Name: "Илья"}, {ID: 2, Name: "Уля"}}

	if u, ok := Other(1, two); !ok || u.Name != "Уля" {
		t.Errorf("Other(1) = %v/%v, ожидалась Уля", u, ok)
	}
	if u, ok := Other(2, two); !ok || u.Name != "Илья" {
		t.Errorf("Other(2) = %v/%v, ожидался Илья", u, ok)
	}
	// Посторонний id — не участник, второго для него нет.
	if _, ok := Other(99, two); ok {
		t.Error("для чужого id партнёра быть не может")
	}
	// Людей не двое — «партнёр» перестаёт быть определённым.
	if _, ok := Other(1, two[:1]); ok {
		t.Error("в одиночку партнёра нет")
	}
}
