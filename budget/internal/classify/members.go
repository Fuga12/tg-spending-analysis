package classify

import (
	"fmt"
	"strings"

	"budget/internal/storage"
)

// Everyone — как модель говорит «трата общая, на всю группу».
//
// Словами, а не пустым массивом: пустой список для модели неотличим от «я не
// понял», и она возвращала бы его в обоих случаях. Значение выглядит как
// фраза, а не как имя, — участника, названного «на всех», не бывает.
const Everyone = "на всех"

// Roster — участники группы под именами, которыми их зовёт модель.
//
// Имя человека принадлежит ему, а не группе, и внутри группы может
// повториться: две Ани в компании из десяти — обычное дело. Модели нужен
// однозначный перечень, поэтому совпавшие имена разводятся номером.
// Настоящее лекарство — не давать заводить двух Ань, но это дело приложения.
type Roster struct {
	labels  []string
	byLabel map[string]int64 // подпись → member_id
	byID    map[int64]string
}

// NewRoster строит перечень по участникам в порядке вступления.
func NewRoster(members []storage.Member) *Roster {
	r := &Roster{
		labels:  make([]string, 0, len(members)),
		byLabel: make(map[string]int64, len(members)),
		byID:    make(map[int64]string, len(members)),
	}
	seen := map[string]int{}
	for _, m := range members {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = "Без имени"
		}
		seen[name]++
		label := name
		if n := seen[name]; n > 1 {
			label = fmt.Sprintf("%s (%d)", name, n)
		}
		r.labels = append(r.labels, label)
		r.byLabel[label] = m.ID
		r.byID[m.ID] = label
	}
	return r
}

// Labels — подписи участников для перечисления в промпте и схеме.
func (r *Roster) Labels() []string { return r.labels }

// Enum — то же плюс «на всех»: полный список допустимых значений получателя.
func (r *Roster) Enum() []string {
	return append(append(make([]string, 0, len(r.labels)+1), r.labels...), Everyone)
}

// Member находит участника по подписи. Регистр не важен: модель охотно
// возвращает «аня» вместо «Аня», и терять из-за этого получателя незачем.
func (r *Roster) Member(label string) (int64, bool) {
	label = strings.TrimSpace(label)
	if id, ok := r.byLabel[label]; ok {
		return id, true
	}
	for known, id := range r.byLabel {
		if strings.EqualFold(known, label) {
			return id, true
		}
	}
	return 0, false
}

// Label — как зовут участника в промпте. Пустая строка, если такого нет.
func (r *Roster) Label(id int64) string { return r.byID[id] }

// Has — состоит ли участник в группе. Адресат категории мог из неё выйти.
func (r *Roster) Has(id int64) bool {
	_, ok := r.byID[id]
	return ok
}

// RememberedRecipient — какого получателя словарь запомнит вместе со словом.
//
// Только когда он ровно один: «косметика — Уле» запоминается, «продукты на
// всех» — нет, для списка в word_map нет колонки. Nil означает «взять
// умолчание категории», и это то же правило, по которому работает разбор.
func RememberedRecipient(item Item) *int64 {
	if len(item.Recipients) != 1 {
		return nil
	}
	id := item.Recipients[0]
	return &id
}

// IsEveryone — сказала ли модель «на всех». Проверяется после Member:
// участник, названный этой фразой, реальнее выдуманного нами значения.
func IsEveryone(label string) bool {
	return strings.EqualFold(strings.TrimSpace(label), Everyone)
}
