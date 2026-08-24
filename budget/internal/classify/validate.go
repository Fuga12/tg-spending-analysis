package classify

import (
	"log/slog"
	"strings"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
	"budget/internal/tokens"
)

// Validate приводит ответ модели к тому, что можно записать в базу.
// Выполняется всегда, даже если ответ прошёл JSON-схему: схема гарантирует
// форму, но не смысл.
func Validate(raw []RawItem, req Request, log *slog.Logger) []Item {
	text := req.Text
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
			Kind:        r.Kind,
			DaysAgo:     r.DaysAgo,
		}

		// 3. Категория должна быть из списка группы, иначе «Прочее».
		cat := categoryByName(req.Cats, strings.TrimSpace(r.Category))
		if cat == nil {
			cat = FallbackCategory(req.Cats)
		}
		if cat != nil {
			id := cat.ID
			item.CategoryID = &id
		}

		// 4. Значение вне перечисления — к безопасному умолчанию.
		if !isKind(item.Kind) {
			item.Kind = KindExpense
		}

		// 5. Получатели — только участники этой группы. Есть они лишь у траты:
		//    у дохода и перевода получателя не бывает.
		if item.Kind == KindExpense {
			item.Recipients = recipients(r, req, cat, log)
		}

		// 6. Перевод — это не трата на категорию.
		if item.Kind == KindTransfer {
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

// recipients разрешает имена, которые вернула модель, в участников группы.
//
// Правило важнее самой модели: если про получателя в сообщении не сказано,
// его даёт умолчание категории, а не догадка по смыслу. «Такси — себе»,
// «Косметика — Уле» люди настраивают под свои привычки, и это не должно
// меняться от формулировки к формулировке.
func recipients(r RawItem, req Request, cat *storage.Category, log *slog.Logger) []int64 {
	if !r.RecipientsStated {
		return defaultRecipients(req, cat)
	}

	out := make([]int64, 0, len(r.Recipients))
	seen := map[int64]bool{}
	everyone := false
	for _, label := range r.Recipients {
		// Участника ищем раньше, чем служебное «на всех»: человек, названный
		// этой фразой, реальнее выдуманного нами значения.
		if id, ok := req.Roster.Member(label); ok {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
			continue
		}
		if IsEveryone(label) {
			everyone = true
			continue
		}
		log.Warn("модель назвала получателя, которого нет в группе",
			"raw_text", req.Text, "получатель", label)
	}

	// «На всех» перебивает перечисление: трата, названная общей, общая
	// целиком, а не в части.
	if everyone {
		return nil
	}
	if len(out) == 0 {
		// Модель сказала, что получатель назван, но назвала одних незнакомцев.
		// Верить такому нельзя — остаётся умолчание.
		return defaultRecipients(req, cat)
	}
	return out
}

// defaultRecipients — на кого уходит трата, о получателе которой не сказано.
//
// Умолчание категории, если оно задано: «Косметика — Уле» верно и когда платит
// не Уля, «Продукты — на всех» верно и когда в сообщении одно слово с суммой.
// Иначе плательщик: назвать трату общей самим значит утверждать то, чего никто
// не говорил, — а «Общее» в отчёте стоит отдельной строкой, и деньги в ней уже
// никому не приписаны. Это рассуждение против догадки, а не против настройки:
// человек, поставивший «на всю группу», как раз это и сказал.
func defaultRecipients(req Request, cat *storage.Category) []int64 {
	if cat != nil && cat.Default.Common {
		return nil
	}
	// Адресат категории мог выйти из группы — тогда умолчание не действует.
	if cat != nil && cat.Default.MemberID != nil && req.Roster.Has(*cat.Default.MemberID) {
		return []int64{*cat.Default.MemberID}
	}
	if req.Payer.ID == 0 {
		return nil
	}
	return []int64{req.Payer.ID}
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

func isKind(s string) bool {
	return s == KindExpense || s == KindIncome || s == KindTransfer
}
