package bot

import (
	"context"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"budget/internal/classify"
	"budget/internal/storage"
)

// onBeneficiary меняет, на кого потрачено, и запоминает выбор как ручной:
// ручная правка приоритетнее ответа модели и не перезатирается (§8).
func (b *Bot) onBeneficiary(beneficiary string) tele.HandlerFunc {
	return func(c tele.Context) error {
		ctx, cancel := b.ctx()
		defer cancel()

		txID, err := strconv.ParseInt(callbackData(c), 10, 64)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Не понял, какая это трата"})
		}

		ok, err := b.store.SetBeneficiary(ctx, txID, c.Sender().ID, beneficiary)
		if err != nil {
			b.log.Error("смена бенефициара", "err", err, "tx", txID)
			return c.Respond(&tele.CallbackResponse{Text: "База не отвечает"})
		}
		if !ok {
			return c.Respond(&tele.CallbackResponse{Text: "Это не твоя трата"})
		}

		tx, err := b.store.Transaction(ctx, txID)
		if err != nil {
			b.log.Error("чтение траты", "err", err, "tx", txID)
			return c.Respond(&tele.CallbackResponse{Text: "База не отвечает"})
		}
		b.rememberChoice(ctx, c.Sender().ID, tx)

		if err := b.edit(c, tx); err != nil {
			return err
		}
		return c.Respond()
	}
}

// onCategoryOpen заменяет клавиатуру сеткой категорий (§9).
func (b *Bot) onCategoryOpen(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	txID, err := strconv.ParseInt(callbackData(c), 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "Не понял, какая это трата"})
	}

	tx, err := b.store.Transaction(ctx, txID)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "Трата не найдена"})
	}
	if tx.PayerID != c.Sender().ID {
		return c.Respond(&tele.CallbackResponse{Text: "Это не твоя трата"})
	}

	cats, err := b.store.Categories(ctx)
	if err != nil {
		b.log.Error("чтение категорий", "err", err)
		return c.Respond(&tele.CallbackResponse{Text: "База не отвечает"})
	}
	if err := c.Edit(transactionLine(tx, time.Now(), b.cfg.TZ), categoryKeyboard(txID, cats)); err != nil {
		return err
	}
	return c.Respond()
}

// onCategoryPick ставит выбранную категорию и возвращает обычную клавиатуру.
func (b *Bot) onCategoryPick(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	txPart, catPart, found := strings.Cut(callbackData(c), ":")
	if !found {
		return c.Respond(&tele.CallbackResponse{Text: "Не понял выбор"})
	}
	txID, err1 := strconv.ParseInt(txPart, 10, 64)
	catID, err2 := strconv.ParseInt(catPart, 10, 32)
	if err1 != nil || err2 != nil {
		return c.Respond(&tele.CallbackResponse{Text: "Не понял выбор"})
	}

	ok, err := b.store.SetCategory(ctx, txID, c.Sender().ID, int32(catID))
	if err != nil {
		b.log.Error("смена категории", "err", err, "tx", txID)
		return c.Respond(&tele.CallbackResponse{Text: "База не отвечает"})
	}
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: "Это не твоя трата"})
	}

	tx, err := b.store.Transaction(ctx, txID)
	if err != nil {
		b.log.Error("чтение траты", "err", err, "tx", txID)
		return c.Respond(&tele.CallbackResponse{Text: "База не отвечает"})
	}
	b.rememberChoice(ctx, c.Sender().ID, tx)

	if err := b.edit(c, tx); err != nil {
		return err
	}
	return c.Respond()
}

// onDelete — удаление мягкое, сообщение остаётся как след (§9).
func (b *Bot) onDelete(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	txID, err := strconv.ParseInt(callbackData(c), 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "Не понял, какая это трата"})
	}

	ok, err := b.store.DeleteTransaction(ctx, txID, c.Sender().ID)
	if err != nil {
		b.log.Error("удаление траты", "err", err, "tx", txID)
		return c.Respond(&tele.CallbackResponse{Text: "База не отвечает"})
	}
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: "Это не твоя трата"})
	}

	if err := c.Edit("🗑 удалено"); err != nil {
		return err
	}
	return c.Respond()
}

// rememberChoice запоминает ручную правку в личном кэше слов.
func (b *Bot) rememberChoice(ctx context.Context, userID int64, tx storage.Transaction) {
	if tx.CategoryID == nil || tx.Kind == classify.KindTransfer {
		return
	}
	for _, w := range classify.SignificantWords(tx.Description) {
		if err := b.store.UpsertWord(ctx, userID, w, *tx.CategoryID, tx.Beneficiary, storage.SourceManual); err != nil {
			b.log.Warn("не запомнил ручную правку", "err", err, "word", w)
		}
	}
}

// edit переписывает исходное сообщение, а не шлёт новое (§9).
func (b *Bot) edit(c tele.Context, tx storage.Transaction) error {
	return c.Edit(transactionLine(tx, time.Now(), b.cfg.TZ), keyboardFor(tx))
}

// callbackData — полезная часть данных коллбэка без служебного префикса.
func callbackData(c tele.Context) string {
	return strings.TrimSpace(c.Callback().Data)
}
