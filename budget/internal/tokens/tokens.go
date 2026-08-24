// Package tokens извлекает суммы из свободного текста сообщения.
//
// Это детерминированная часть разбора: что нашлось здесь — единственный
// источник правды о суммах. Ответ модели сверяется с этим множеством,
// поэтому пакет не должен ничего досочинять.
package tokens

import (
	"strings"
	"unicode"

	"github.com/shopspring/decimal"
)

// match — найденная сумма и её место в тексте (в рунах, не в байтах).
type match struct {
	Value decimal.Decimal
	Start int
	End   int
}

var thousand = decimal.NewFromInt(1000)

// Extract возвращает все суммы из текста в порядке появления.
// Дубликаты сохраняются: «1200 и ещё 1200» — это две суммы.
func Extract(s string) []decimal.Decimal {
	found := find(s)
	out := make([]decimal.Decimal, 0, len(found))
	for _, m := range found {
		out = append(out, m.Value)
	}
	return out
}

// Strip убирает из текста сами суммы вместе с валютными хвостами. Нужен для
// описания траты: «600 лимонад» → «лимонад».
func Strip(s string) string {
	r := []rune(s)
	found := find(s)
	var b strings.Builder
	prev := 0
	for _, m := range found {
		b.WriteString(string(r[prev:m.Start]))
		b.WriteRune(' ')
		prev = m.End
	}
	b.WriteString(string(r[prev:]))
	// Пробелы схлопываются: на месте вырезанных сумм иначе остаются дыры.
	return strings.Join(strings.Fields(b.String()), " ")
}

func find(s string) []match {
	r := []rune(s)
	var out []match

	for i := 0; i < len(r); {
		if !isDigit(r[i]) {
			i++
			continue
		}
		// Цифра приклеена к буквам слева. Одиночная — часть слова («теле2»),
		// две и больше — слипшаяся сумма («такси400»), её терять нельзя.
		if i > 0 && unicode.IsLetter(r[i-1]) && digitRunLen(r, i) < 2 {
			i = skipWord(r, i)
			continue
		}

		start := i
		intPart := readDigits(r, &i)

		// «19:30» — время, а не сумма. Иначе деградированный путь
		// запишет трату на 19 ₽.
		if isTimeTail(r, i) {
			i = skipTimeTail(r, i)
			continue
		}

		// Пробел как разделитель тысяч: «1 200» — одна сумма, а не две.
		// Группа справа — ровно три цифры, голова слева — не больше трёх,
		// иначе «1200 400» слиплось бы в 1 200 400.
		if len(intPart) <= 3 {
			for {
				j := i
				if j < len(r) && isSpace(r[j]) {
					j++
					k := j
					for k < len(r) && isDigit(r[k]) {
						k++
					}
					if k-j == 3 {
						intPart += string(r[j:k])
						i = k
						continue
					}
				}
				break
			}
		}

		num := intPart
		if i+1 < len(r) && (r[i] == '.' || r[i] == ',') && isDigit(r[i+1]) {
			i++
			num += "." + readDigits(r, &i)
		}

		value, err := decimal.NewFromString(num)
		if err != nil {
			continue
		}

		// «3к», «1.5к», «5 тыс» — тысячи. Но «3кг» — это вес, а не 3000.
		if i < len(r) && isThousandSuffix(r[i]) && (i+1 >= len(r) || !unicode.IsLetter(r[i+1])) {
			value = value.Mul(thousand)
			i++
		} else if end, ok := thousandWord(r, i); ok {
			value = value.Mul(thousand)
			i = end
		}

		// Латинская буква сразу за числом — это «4g», «5gb», а не сумма.
		if i < len(r) && isLatin(r[i]) {
			i = skipWord(r, i)
			continue
		}
		// Валютный хвост проглатывается вместе с суммой, чтобы он не оседал
		// в описании при деградированном разборе.
		i = skipCurrency(r, i)

		out = append(out, match{Value: value, Start: start, End: i})
	}
	return out
}

// digitRunLen — длина цепочки цифр, начинающейся с позиции i.
func digitRunLen(r []rune, i int) int {
	n := 0
	for i+n < len(r) && isDigit(r[i+n]) {
		n++
	}
	return n
}

// skipWord перематывает за конец буквенно-цифрового слова.
func skipWord(r []rune, i int) int {
	for i < len(r) && (isDigit(r[i]) || unicode.IsLetter(r[i])) {
		i++
	}
	return i
}

func isTimeTail(r []rune, i int) bool {
	return i+1 < len(r) && r[i] == ':' && isDigit(r[i+1])
}

func skipTimeTail(r []rune, i int) int {
	for isTimeTail(r, i) {
		i++
		for i < len(r) && isDigit(r[i]) {
			i++
		}
	}
	return i
}

// thousandWord распознаёт «тыс», «тысяч» и родню после числа, в том числе
// отделённые пробелом. Одиночное «к» здесь сознательно не поддержано: в
// «300 к чаю» это предлог, и ошибка стоила бы множителя в тысячу раз.
func thousandWord(r []rune, i int) (int, bool) {
	j := i
	if j < len(r) && isSpace(r[j]) {
		j++
	}
	k := j
	for k < len(r) && unicode.IsLetter(r[k]) {
		k++
	}
	switch strings.ToLower(string(r[j:k])) {
	case "тыс", "тысяч", "тысячи", "тысяча", "тысячу":
		if k < len(r) && r[k] == '.' {
			k++
		}
		return k, true
	}
	return i, false
}

func readDigits(r []rune, i *int) string {
	start := *i
	for *i < len(r) && isDigit(r[*i]) {
		*i++
	}
	return string(r[start:*i])
}

// skipCurrency сдвигает позицию за «₽», «р», «руб», «рублей» и латинские
// аналоги, в том числе отделённые пробелом («500 руб.»).
func skipCurrency(r []rune, i int) int {
	start := i
	if i < len(r) && isSpace(r[i]) {
		i++
	}
	if i < len(r) && r[i] == '₽' {
		return i + 1
	}
	j := i
	for j < len(r) && unicode.IsLetter(r[j]) {
		j++
	}
	switch strings.ToLower(string(r[i:j])) {
	case "р", "руб", "рубль", "рубля", "рублей", "r", "rub":
		if j < len(r) && r[j] == '.' {
			j++
		}
		return j
	}
	return start
}

func isDigit(c rune) bool { return c >= '0' && c <= '9' }

func isLatin(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isSpace — пробелы, которые реально прилетают из Telegram: обычный,
// неразрывный, узкий неразрывный, тонкий, плюс табуляция.
func isSpace(c rune) bool {
	switch c {
	case ' ', '\u00a0', '\u202f', '\u2009', '\t':
		return true
	}
	return false
}

func isThousandSuffix(c rune) bool {
	switch c {
	case 'к', 'К', 'k', 'K':
		return true
	}
	return false
}
