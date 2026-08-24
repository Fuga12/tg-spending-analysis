package bot

import (
	"context"
	"errors"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"budget/internal/classify"
	"budget/internal/storage"
)

const greeting = `Привет! Я веду общий бюджет.

Просто пиши тратами, как говоришь:
  600 лимонад
  такси 450
  вчера взял в пятёрочке на 1200 и такси 400

Формат — в /помощь.`

// appHint — приписка к приветствию, когда приложение есть. Отдельно от
// greeting: обещать отчёты и участников, пока их негде показать, нельзя.
const appHint = "\n\nОтчёты, категории и участники — в приложении."

const noAmountReply = "Не вижу сумму. Например: 600 лимонад"

// noGroup — человек боту известен, но ни в какой группе не состоит.
// Записывать его траты некуда: бюджет принадлежит группе, а не человеку.
//
// Совет зависит от того, есть ли уже приложение: посылать создавать группу
// туда, куда нельзя попасть, — это тупик, а не подсказка.
func noGroup(hasApp bool) string {
	const head = "Ты пока не в группе — записывать траты некуда.\n\n"
	if hasApp {
		return head + "Создай свою в приложении или попроси администратора добавить тебя."
	}
	return head + "Попроси администратора группы добавить тебя."
}

func (b *Bot) onStart(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	if err := b.store.EnsureUser(ctx, c.Sender().ID, displayName(c.Sender())); err != nil {
		b.log.Error("запись участника", "err", err, "user_id", c.Sender().ID)
		return c.Send("Не смог записать тебя в базу, попробуй ещё раз.")
	}

	_, ok, err := b.store.MemberOf(ctx, c.Sender().ID)
	if err != nil {
		b.log.Error("поиск группы", "err", err, "user_id", c.Sender().ID)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}
	if !ok {
		return c.Send(noGroup(b.cfg.HasApp()))
	}

	markup := appMarkup(b.cfg.AppURL)
	text := greeting
	if markup != nil {
		text += appHint
	}
	return c.Send(text, sendOptions(markup)...)
}

// onText — основной сценарий: свободный текст превращается в траты.
func (b *Bot) onText(c tele.Context) error {
	sender := c.Sender()
	text := strings.TrimSpace(c.Text())

	// Сообщение, начинающееся со слэша, — команда, а не трата.
	if strings.HasPrefix(text, "/") {
		return b.dispatchCommand(c, text)
	}

	userCtx, cancelUser := b.ctx()
	// Пользователь мог начать с траты, не нажав /start.
	err := b.store.EnsureUser(userCtx, sender.ID, displayName(sender))
	var (
		member  storage.Member
		inGroup bool
	)
	if err == nil {
		member, inGroup, err = b.store.MemberOf(userCtx, sender.ID)
	}
	cancelUser()
	if err != nil {
		b.log.Error("запись участника", "err", err, "user_id", sender.ID)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}
	if !inGroup {
		return c.Send(noGroup(b.cfg.HasApp()))
	}
	group := b.store.ForGroup(member.GroupID)

	// Потолок стоит до разбора, а не после: смысл в том, чтобы не ходить
	// в сеть, а не в том, чтобы не сохранять.
	if !b.limiter.allow(sender.ID) {
		b.log.Warn("суточный потолок сообщений исчерпан", "user_id", sender.ID)
		return c.Send("На сегодня хватит — столько сообщений за сутки я не разбираю. Завтра снова.")
	}

	classifyCtx, cancelClassify := b.classifyCtx()
	res, err := b.classifier.Classify(classifyCtx, classify.Scope{
		Payer: member, Dict: group, Usage: group,
		Quota: b.groupQuota(group),
	}, text)
	cancelClassify()
	if err != nil {
		if errors.Is(err, classify.ErrNoAmount) {
			return c.Send(noAmountReply)
		}
		b.log.Error("разбор сообщения", "err", err, "user_id", sender.ID)
		return c.Send("Не смог разобрать сообщение. Попробуй иначе: 600 лимонад")
	}

	// Запись идёт на свежем контексте: сколько бы ни думала модель, на
	// сохранение разобранного времени должно хватить.
	ctx, cancel := b.ctx()
	defer cancel()

	cats, err := group.Categories(ctx)
	if err != nil {
		b.log.Warn("не прочитал категории", "err", err)
	}

	now := time.Now()
	for _, item := range res.Items {
		tx, err := b.save(ctx, group, member, text, item, cats, now)
		if err != nil {
			b.log.Error("запись траты", "err", err, "user_id", sender.ID, "raw_text", text)
			if sendErr := c.Send("Не смог записать трату. Напиши ещё раз."); sendErr != nil {
				b.log.Error("не отправил ответ", "err", sendErr)
			}
			continue
		}
		// Ошибка отправки одного ответа не должна лишать пользователя
		// остальных: транзакции уже в базе.
		if err := c.Send(transactionLine(tx, now, b.cfg.TZ), txMarkup(tx.ID)); err != nil {
			b.log.Error("не отправил ответ по трате", "err", err, "tx", tx.ID)
		}
	}
	return nil
}

func (b *Bot) onHelp(c tele.Context) error {
	return c.Send(helpText)
}

// dispatchCommand разбирает команду сам: до обработчиков telebot доезжают
// только команды без аргументов и только латиницей (см. routes).
func (b *Bot) dispatchCommand(c tele.Context, text string) error {
	name, payload := parseCommand(text)

	handler, ok := b.commands[name]
	if !ok {
		return c.Send("Не знаю такой команды. Что умею — в /помощь")
	}
	if msg := c.Message(); msg != nil {
		msg.Payload = payload
	}
	return handler(c)
}

// parseCommand делит «/месяц@budget_bot 6» на имя команды и аргумент.
func parseCommand(text string) (name, payload string) {
	name, payload, _ = strings.Cut(strings.TrimSpace(text), " ")
	// «@budget_bot» — обращение к боту по имени, в группах Telegram его
	// добавляет сам.
	name, _, _ = strings.Cut(name, "@")
	return strings.ToLower(name), strings.TrimSpace(payload)
}

// groupQuota — месячный потолок токенов этой группы. Nil, если потолок
// выключен: до открытия бота считать по группам незачем.
func (b *Bot) groupQuota(g *storage.GroupStore) classify.Budgetable {
	if b.cfg.LLMGroupTokenBudget <= 0 {
		return nil
	}
	return classify.NewBudget(b.cfg.LLMGroupTokenBudget, g, nil, b.log)
}

// save записывает трату и запоминает слова описания в личном словаре.
func (b *Bot) save(ctx context.Context, g *storage.GroupStore, member storage.Member,
	raw string, item classify.Item, cats []storage.Category, now time.Time) (storage.Transaction, error) {
	tx := storage.Transaction{
		GroupID:             member.GroupID,
		PayerMemberID:       member.ID,
		PayerUserID:         member.UserID,
		Kind:                item.Kind,
		Amount:              item.Amount,
		Description:         item.Description,
		CategoryID:          item.CategoryID,
		RawText:             raw,
		Recipients:          item.Recipients,
		NeedsClassification: item.NeedsClassification,
		SpentAt:             now.AddDate(0, 0, -item.DaysAgo),
	}

	id, err := g.InsertTransaction(ctx, tx)
	if err != nil {
		return storage.Transaction{}, err
	}
	tx.ID = id

	if item.CategoryID != nil {
		tx.CategoryName = categoryName(cats, *item.CategoryID)
		b.rememberWords(ctx, g, member.UserID, item)
	}
	return tx, nil
}

// rememberWords кладёт слова описания в личный словарь, чтобы в следующий раз
// ответ пришёл мгновенно и без обращения к API.
//
// Запоминаются только расходы: быстрый путь всегда собирает expense, и
// запомненный доход или перевод во второй раз записался бы тратой.
func (b *Bot) rememberWords(ctx context.Context, g *storage.GroupStore, userID int64, item classify.Item) {
	if item.CategoryID == nil || item.Kind != classify.KindExpense {
		return
	}
	member := classify.RememberedRecipient(item)
	for _, w := range item.Words {
		if err := g.UpsertWord(ctx, userID, w, *item.CategoryID, member, storage.SourceLLM); err != nil {
			b.log.Warn("не запомнил слово", "err", err, "word", w)
		}
	}
}

func categoryName(cats []storage.Category, id int32) string {
	for _, c := range cats {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// displayName — имя из профиля Telegram, которым пользователь заводится
// в первый раз. Дальше его можно поменять в приложении, и оно не
// перезатирается: см. storage.EnsureUser.
func displayName(u *tele.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	if name == "" {
		name = "Без имени"
	}
	return name
}
