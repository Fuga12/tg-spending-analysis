package classify

import (
	"context"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
	"budget/internal/tokens"
)

// Слова, при которых кэш не срабатывает: они означают дату, несколько трат
// или иную сложность, с которой словарь без контекста не справится (§5).
var complexityTriggers = map[string]bool{
	"вчера":     true,
	"позавчера": true,
	"утром":     true,
	"днём":      true,
	"днем":      true,
	"и":         true,
}

// Стоп-лист незначимых слов (§5).
var stopWords = map[string]bool{
	"для": true, "что": true, "как": true, "там": true, "тут": true,
	"это": true, "при": true, "над": true, "под": true, "без": true,
	"про": true, "из-за": true, "на": true, "за": true,
}

// resolveFromCache — быстрый путь: ответ собирается локально, в сеть не идём.
// Второе возвращаемое значение — сработал ли путь.
func (s *Service) resolveFromCache(
	ctx context.Context,
	userID int64,
	text string,
	amounts []decimal.Decimal,
	cats []storage.Category,
) (*Result, bool, error) {
	// Условие 1: ровно одна сумма.
	if len(amounts) != 1 {
		return nil, false, nil
	}
	// Условие 2: нет слов-триггеров сложности и разделителей перечисления.
	if hasComplexity(text) {
		return nil, false, nil
	}

	// Слова берутся из описания, то есть из текста без сумм: иначе «кофе 500р»
	// даёт лишнее слово «500р», которого нет ни в одном словаре, и быстрый
	// путь не срабатывает никогда.
	description := Describe(text)
	words := SignificantWords(description)
	if len(words) == 0 {
		return nil, false, nil
	}

	hits, err := s.dict.LookupWords(ctx, userID, words)
	if err != nil {
		return nil, false, err
	}
	// Условие 3: каждое значимое слово должно быть найдено.
	for _, w := range words {
		if _, ok := hits[w]; !ok {
			return nil, false, nil
		}
	}

	best, ok := bestHit(words, hits)
	if !ok {
		return nil, false, nil
	}
	cat := categoryByID(cats, best.CategoryID)
	if cat == nil {
		return nil, false, nil
	}

	ben := best.Beneficiary
	if ben == "" {
		ben = cat.DefaultBeneficiary
	}

	id := cat.ID
	return &Result{
		Source: SourceCache,
		Items: []Item{{
			Amount:      amounts[0],
			Description: description,
			CategoryID:  &id,
			Beneficiary: ben,
			Kind:        KindExpense,
			DaysAgo:     0,
			// В кэш возвращаются только слова, которые и так указывали на
			// категорию-победителя. Иначе «утренний самокат» перепривязал бы
			// слово «утренний» к категории самоката — новых знаний здесь нет.
			Words: wordsOfCategory(words, hits, best.CategoryID),
		}},
	}, true, nil
}

func wordsOfCategory(words []string, hits map[string]storage.WordHit, categoryID int32) []string {
	var out []string
	for _, w := range words {
		if h, ok := hits[w]; ok && h.CategoryID == categoryID {
			out = append(out, w)
		}
	}
	return out
}

// bestHit выбирает победителя, когда значимые слова разошлись в категориях:
// ручная правка важнее ответа модели, ответ модели важнее общей затравки,
// внутри личного кэша — слово с максимальным hits (§5).
func bestHit(words []string, hits map[string]storage.WordHit) (storage.WordHit, bool) {
	var best storage.WordHit
	found := false
	for _, w := range words {
		h, ok := hits[w]
		if !ok {
			continue
		}
		if !found || less(best, h) {
			best, found = h, true
		}
	}
	return best, found
}

// less сообщает, что a слабее b.
func less(a, b storage.WordHit) bool {
	if rank(a.Source) != rank(b.Source) {
		return rank(a.Source) < rank(b.Source)
	}
	return a.Hits < b.Hits
}

func rank(source string) int {
	switch source {
	case storage.SourceManual:
		return 3
	case storage.SourceLLM:
		return 2
	default:
		return 1
	}
}

func hasComplexity(text string) bool {
	if strings.ContainsAny(text, ",;\n") {
		return true
	}
	for _, w := range splitWords(text) {
		if complexityTriggers[w] {
			return true
		}
	}
	return false
}

// SignificantWords — слова описания, по которым работает словарь: нижний
// регистр, длина не меньше трёх символов, не из стоп-листа (§5).
func SignificantWords(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range splitWords(text) {
		if len([]rune(w)) < 3 || stopWords[w] || seen[w] {
			continue
		}
		// Слово без единой буквы — это остаток числа, а не слово описания:
		// в словарь такому попадать незачем.
		if !containsLetter(w) {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// Describe — текст без сумм, годный как описание траты (§8).
func Describe(text string) string {
	return trimTo(strings.Join(strings.Fields(tokens.Strip(text)), " "), 64)
}

func splitWords(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '-'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, "-")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func containsLetter(s string) bool {
	for _, c := range s {
		if unicode.IsLetter(c) {
			return true
		}
	}
	return false
}

// trimTo обрезает строку до n символов, не разрывая руны.
func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}
