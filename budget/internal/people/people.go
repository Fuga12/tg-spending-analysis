// Package people знает, как назвать участников бюджета.
//
// «на неё» и «ей» написаны с одной точки зрения — того, кто платил. Второй
// человек читает те же слова про себя и понимает их наоборот. Имена такой
// двусмысленности не допускают, поэтому подписи собираются из имён.
package people

import (
	"strings"
	"unicode"

	"budget/internal/storage"
)

// Dative — имя в дательном падеже: «Уля» → «Уле», «Илья» → «Илье».
//
// Полноценная морфология здесь не нужна и не нужна нигде поблизости:
// участников двое, это личные имена, и правил для них хватает пяти.
// Незнакомое окончание оставляем как есть — неловкое «Ким» лучше выдуманного.
func Dative(name string) string {
	name = strings.TrimSpace(name)
	r := []rune(name)
	if len(r) == 0 || !hasCyrillic(r) {
		return name
	}

	head := string(r[:len(r)-1])
	switch last := r[len(r)-1]; {
	case strings.HasSuffix(name, "ия"): // Мария → Марии
		return head + "и"
	case last == 'я', last == 'а': // Уля → Уле, Илья → Илье, Маша → Маше
		return head + "е"
	case last == 'й', last == 'ь': // Андрей → Андрею, Игорь → Игорю
		return head + "ю"
	case isConsonant(last): // Иван → Ивану
		return name + "у"
	}
	return name
}

// Other — второй участник бюджета. Бот рассчитан на двоих; при ином числе
// людей «партнёр» перестаёт быть определённым, и мы это признаём.
func Other(userID int64, users []storage.User) (storage.User, bool) {
	if len(users) != 2 {
		return storage.User{}, false
	}
	if users[0].ID == userID {
		return users[1], true
	}
	if users[1].ID == userID {
		return users[0], true
	}
	return storage.User{}, false
}

func hasCyrillic(r []rune) bool {
	for _, c := range r {
		if unicode.Is(unicode.Cyrillic, c) {
			return true
		}
	}
	return false
}

func isConsonant(c rune) bool {
	return strings.ContainsRune("бвгджзйклмнпрстфхцчшщ", unicode.ToLower(c))
}
