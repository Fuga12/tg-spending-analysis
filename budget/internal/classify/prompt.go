package classify

import (
	"strconv"
	"strings"

	"budget/internal/storage"
)

// SystemPrompt собирает системное сообщение под конкретную группу: правила,
// состав, категории и примеры разбора с настоящими именами.
//
// Экспортирован ради тестов: промпт — единственное место, где состояние
// группы превращается в текст, и молча разъехаться с валидацией он не должен.
func SystemPrompt(req Request) string {
	return promptRules + groupBlock(req) + categoryBlock(req.Cats) + examplesBlock(req)
}

// promptRules — правила разбора, одни на все группы.
const promptRules = `Ты разбираешь короткие сообщения о тратах общего бюджета на русском языке. Пишут разговорно, с сокращениями и опечатками.

Правила:
- Одно сообщение может содержать несколько трат — верни их отдельными элементами массива items.
- amount — число ровно так, как записано в сообщении. Не пересчитывай его и не меняй разрядность. «к» означает тысячи: «5к» это 5000.
- description — 1-3 слова по сути траты, без суммы и без указания, кому она.
- recipients — кому досталась трата. «себе», «мне» — автор сообщения. «нам», «всем», «на всех», «домой» — общая трата. Имя, «ей», «ему» — тот участник, о котором речь.
- recipients_stated: true, только если про получателя сказано прямо. Догадка по смыслу — это false; тогда оставь recipients пустым, получателя подставят без тебя.
- kind: transfer — автор передал деньги другому человеку, а не купил что-то. income — поступление денег. В остальных случаях expense.
- days_ago: 0, если про день ничего не сказано; 1 для «вчера»; 2 для «позавчера».`

// groupBlock называет модели автора сообщения и остальных участников.
// Без автора не разобрать «себе», без списка — «Ане».
func groupBlock(req Request) string {
	var b strings.Builder
	b.WriteString("\n\nСообщение пишет: " + req.Payer.Name)
	b.WriteString("\nУчастники группы: " + strings.Join(req.Roster.Labels(), ", "))
	b.WriteString("\nВ recipients можно называть только их и теми же именами. " +
		"Общая трата — это «" + Everyone + "».")
	return b.String()
}

// categoryBlock перечисляет категории списком в конце системного промпта.
//
// Подсказки лежат и в описании поля схемы, но там они слипаются в одну
// длинную строку и тонут: «пиво» уходило в Продукты при живой категории
// «Алкоголь — пиво, вино и тп». Списком модель их читает.
func categoryBlock(cats []storage.Category) string {
	var b strings.Builder
	b.WriteString("\n\nКатегории и что к ним относится:\n")
	for _, c := range cats {
		b.WriteString("- " + c.Name)
		if c.Hint != "" {
			b.WriteString(" — " + c.Hint)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nЕсли трата подходит под подсказку конкретной категории, выбирай её, " +
		"даже когда подходит и более общая. «Пиво» при наличии категории про алкоголь — " +
		"это алкоголь, а не продукты.")
	return b.String()
}

// examplesBlock — few-shot с настоящими именами участников.
//
// Константой он быть перестал: примеры учат модель называть получателя, а
// назвать его можно только именем, которое в этой группе есть. Прежний набор
// был написан про двоих и говорил «ей» — на группе из пяти это учило ровно
// тому, чего делать не надо.
func examplesBlock(req Request) string {
	var b strings.Builder
	b.WriteString("\n\nПримеры разбора:\n\n")

	ex := func(text string, items ...string) {
		b.WriteString(strconv.Quote(text) + "\n")
		b.WriteString(`{"items":[` + strings.Join(items, ",") + "]}\n\n")
	}

	ex("600 лимонад",
		item(600, "лимонад", "Продукты", nil, false, KindExpense, 0))
	ex("такси 450 себе",
		item(450, "такси", "Такси", []string{req.Payer.Name}, true, KindExpense, 0))
	ex("вчера взял в пятёрочке на 1200 и такси 400 домой",
		item(1200, "пятёрочка", "Продукты", nil, false, KindExpense, 1),
		item(400, "такси", "Такси", nil, false, KindExpense, 1))
	ex("позавчера аптека 780",
		item(780, "аптека", "Здоровье", nil, false, KindExpense, 2))
	ex("продукты на всех 3200",
		item(3200, "продукты", "Продукты", []string{Everyone}, true, KindExpense, 0))

	// Пример «трата на другого» показывается, только если этот другой есть.
	// Выдуманное имя модель начнёт возвращать как настоящее.
	//
	// Имя стоит в именительном падеже через двоеточие, а не в «купил Ане»:
	// склонять имена участников мы не умеем, а «купил Аня цветы» — это
	// пример на ломаном русском, и учит он ровно ему.
	if other, ok := someoneElse(req); ok {
		ex(other+": цветы 2500",
			item(2500, "цветы", "Подарки", []string{other}, true, KindExpense, 0))
	}

	ex("скинул 5к",
		item(5000, "перевод", "Прочее", nil, false, KindTransfer, 0))
	ex("зарплата 90000",
		item(90000, "зарплата", "Прочее", nil, false, KindIncome, 0))
	ex("жкх 4300 и интернет 700",
		item(4300, "жкх", "Коммуналка", nil, false, KindExpense, 0),
		item(700, "интернет", "Связь и интернет", nil, false, KindExpense, 0))

	b.WriteString("Отвечай только JSON по схеме, без пояснений.")
	return b.String()
}

// item рисует один элемент ответа для few-shot. Собирается кодом, а не руками:
// пример, разошедшийся со схемой хоть одним полем, учит модель отвечать не по
// схеме — и это самый частый режим её поломки.
func item(amount int, description, category string, recipients []string,
	stated bool, kind string, daysAgo int) string {
	names := make([]string, 0, len(recipients))
	for _, r := range recipients {
		names = append(names, strconv.Quote(r))
	}
	return `{"amount":` + strconv.Itoa(amount) +
		`,"description":` + strconv.Quote(description) +
		`,"category":` + strconv.Quote(category) +
		`,"recipients":[` + strings.Join(names, ",") + `]` +
		`,"recipients_stated":` + strconv.FormatBool(stated) +
		`,"kind":` + strconv.Quote(kind) +
		`,"days_ago":` + strconv.Itoa(daysAgo) + `}`
}

// someoneElse — любой участник, кроме автора сообщения.
func someoneElse(req Request) (string, bool) {
	for _, label := range req.Roster.Labels() {
		if id, ok := req.Roster.Member(label); ok && id != req.Payer.ID {
			return label, true
		}
	}
	return "", false
}
