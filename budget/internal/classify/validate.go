package classify

import (
	"log/slog"
	"strings"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
	"budget/internal/tokens"
)

// Validate приводит ответ модели к тому, что можно записать в базу (§8).
// Выполняется всегда, даже если ответ прошёл JSON-схему: схема гарантирует
// форму, но не смысл.
func Validate(raw []RawItem, text string, cats []storage.Category, log *slog.Logger) []Item {
	amounts := tokens.Extract(text)
	out := make([]Item, 0, len(raw))

	for _, r := range raw {
		amount, err := decimal.NewFromString(r.Amount.String())
		if err != nil {
			log.Warn("модель вернула сумму, которую не разобрать",
				"raw_text", text, "amount", r.Amount.String())
			continue
		}
		// 1. Сумма обязана присутствовать среди найденных в тексте.
		//    Модель не считает, она только раскладывает.
		if !containsAmount(amounts, amount) {
			log.Warn("сумма модели не совпала с текстом",
				"raw_text", text, "model_amount", amount.String(), "text_amounts", amountsToStrings(amounts))
			continue
		}
		// 2. Ноль и минус — не трата.
		if !amount.IsPositive() {
			log.Warn("модель вернула неположительную сумму", "raw_text", text, "amount", amount.String())
			continue
		}

		item := Item{
			Amount:      amount,
			Description: trimTo(strings.TrimSpace(r.Description), 64),
			Beneficiary: r.Beneficiary,
			Kind:        r.Kind,
			DaysAgo:     r.DaysAgo,
		}

		// 3. Категория должна быть из таблицы, иначе «Прочее».
		cat := categoryByName(cats, strings.TrimSpace(r.Category))
		if cat == nil {
			cat = categoryByName(cats, CategoryOther)
		}
		if cat != nil {
			id := cat.ID
			item.CategoryID = &id
		}

		// 4-5. Значения вне перечисления — к безопасному умолчанию.
		if !isBeneficiary(item.Beneficiary) {
			item.Beneficiary = BenPayer
		}

		// Если в сообщении про получателя не сказано, беневициара даёт не
		// догадка модели, а умолчание категории: «Такси — на себя», «Продукты —
		// на двоих» настраивается людьми под свои привычки и не меняется от
		// формулировки к формулировке.
		if !r.BeneficiaryStated && cat != nil && isBeneficiary(cat.DefaultBeneficiary) {
			item.Beneficiary = cat.DefaultBeneficiary
		}
		if !isKind(item.Kind) {
			item.Kind = KindExpense
		}

		// 6. Перевод партнёру — это не трата на категорию.
		if item.Kind == KindTransfer {
			item.Beneficiary = BenPartner
			item.CategoryID = nil
		}

		// 7. Дата в разумных пределах.
		if item.DaysAgo < 0 {
			item.DaysAgo = 0
		}
		if item.DaysAgo > 30 {
			item.DaysAgo = 30
		}

		// 8. Пустое описание — из исходного текста.
		if item.Description == "" {
			item.Description = Describe(text)
		}
		if item.Description == "" {
			item.Description = trimTo(strings.TrimSpace(text), 64)
		}

		if item.Kind != KindTransfer {
			item.Words = SignificantWords(item.Description)
		}

		out = append(out, item)
	}
	return out
}

func containsAmount(amounts []decimal.Decimal, want decimal.Decimal) bool {
	for _, a := range amounts {
		if a.Equal(want) {
			return true
		}
	}
	return false
}

func amountsToStrings(amounts []decimal.Decimal) string {
	parts := make([]string, 0, len(amounts))
	for _, a := range amounts {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ",")
}

func isBeneficiary(s string) bool {
	return s == BenPayer || s == BenPartner || s == BenBoth
}

func isKind(s string) bool {
	return s == KindExpense || s == KindIncome || s == KindTransfer
}
