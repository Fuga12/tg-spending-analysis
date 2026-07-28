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

const greeting = `Привет! Я веду наш общий бюджет.

Просто пиши тратами, как говоришь:
  600 лимонад
  такси 450
  вчера взял в пятёрочке на 1200 и такси 400

Команды: /месяц, /день, /лимит, /категории, /помощь`

const noAmountReply = "Не вижу сумму. Например: 600 лимонад"

func (b *Bot) onStart(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	if err := b.store.UpsertUser(ctx, c.Sender().ID, displayName(c.Sender())); err != nil {
		b.log.Error("upsert пользователя", "err", err, "user_id", c.Sender().ID)
		return c.Send("Не смог записать тебя в базу, попробуй ещё раз.")
	}
	return c.Send(greeting)
}

// onText — основной сценарий: свободный текст превращается в траты (§9).
func (b *Bot) onText(c tele.Context) error {
	sender := c.Sender()
	text := strings.TrimSpace(c.Text())

	userCtx, cancelUser := b.ctx()
	err := b.store.UpsertUser(userCtx, sender.ID, displayName(sender))
	cancelUser()
	if err != nil {
		// Пользователь мог начать с траты, не нажав /start.
		b.log.Error("upsert пользователя", "err", err, "user_id", sender.ID)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}

	classifyCtx, cancelClassify := b.classifyCtx()
	res, err := b.classifier.Classify(classifyCtx, sender.ID, text)
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

	cats, err := b.store.Categories(ctx)
	if err != nil {
		b.log.Warn("не прочитал категории", "err", err)
	}

	now := time.Now()
	for _, item := range res.Items {
		tx, err := b.save(ctx, sender.ID, text, item, cats, now)
		if err != nil {
			b.log.Error("запись траты", "err", err, "user_id", sender.ID, "raw_text", text)
			if sendErr := c.Send("Не смог записать трату. Напиши ещё раз."); sendErr != nil {
				b.log.Error("не отправил ответ", "err", sendErr)
			}
			continue
		}
		// Ошибка отправки одного ответа не должна лишать пользователя
		// остальных: транзакции уже в базе, а кнопки приходят только с ними.
		if err := c.Send(transactionLine(tx, now, b.cfg.TZ), keyboardFor(tx)); err != nil {
			b.log.Error("не отправил ответ по трате", "err", err, "tx", tx.ID)
		}
	}
	return nil
}

// save записывает трату и запоминает слова описания в личном кэше (§8).
func (b *Bot) save(ctx context.Context, userID int64, raw string, item classify.Item, cats []storage.Category, now time.Time) (storage.Transaction, error) {
	tx := storage.Transaction{
		PayerID:             userID,
		Beneficiary:         item.Beneficiary,
		Kind:                item.Kind,
		Amount:              item.Amount,
		Description:         item.Description,
		CategoryID:          item.CategoryID,
		RawText:             raw,
		NeedsClassification: item.NeedsClassification,
		SpentAt:             now.AddDate(0, 0, -item.DaysAgo),
	}

	id, err := b.store.InsertTransaction(ctx, tx)
	if err != nil {
		return storage.Transaction{}, err
	}
	tx.ID = id

	if item.CategoryID != nil {
		tx.CategoryName = categoryName(cats, *item.CategoryID)
		b.rememberWords(ctx, userID, item)
	}
	return tx, nil
}

// rememberWords кладёт слова описания в личный кэш, чтобы в следующий раз
// ответ пришёл мгновенно и без обращения к API (§8).
//
// Запоминаются только расходы: быстрый путь всегда собирает expense, и
// запомненный доход или перевод во второй раз записался бы тратой.
func (b *Bot) rememberWords(ctx context.Context, userID int64, item classify.Item) {
	if item.CategoryID == nil || item.Kind != classify.KindExpense {
		return
	}
	for _, w := range item.Words {
		if err := b.store.UpsertWord(ctx, userID, w, *item.CategoryID, item.Beneficiary, storage.SourceLLM); err != nil {
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

// displayName — как звать пользователя в отчётах.
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
