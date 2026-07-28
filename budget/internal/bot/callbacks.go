package bot

import (
	"context"
	"errors"
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

		tx, err := b.owned(ctx, c)
		if err != nil {
			return respond(c, err.Error())
		}

		if _, err := b.store.SetBeneficiary(ctx, tx.ID, tx.PayerID, beneficiary); err != nil {
			b.log.Error("смена бенефициара", "err", err, "tx", tx.ID)
			return respond(c, "База не отвечает")
		}
		tx.Beneficiary = beneficiary
		classify.RememberManual(ctx, b.store, tx, b.log)

		if err := b.edit(c, tx); err != nil {
			return err
		}
		return respond(c, "Теперь "+beneficiaryLabel(beneficiary))
	}
}

// onCategoryOpen заменяет клавиатуру сеткой категорий (§9).
func (b *Bot) onCategoryOpen(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	tx, err := b.owned(ctx, c)
	if err != nil {
		return respond(c, err.Error())
	}

	cats, err := b.store.Categories(ctx)
	if err != nil {
		b.log.Error("чтение категорий", "err", err)
		return respond(c, "База не отвечает")
	}
	if err := editMessage(c, transactionLine(tx, time.Now(), b.cfg.TZ), categoryKeyboard(tx.ID, cats)); err != nil {
		return err
	}
	return c.Respond()
}

// onCategoryPick ставит выбранную категорию и возвращает обычную клавиатуру.
func (b *Bot) onCategoryPick(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	_, catPart, found := strings.Cut(callbackData(c), ":")
	if !found {
		return respond(c, "Не понял выбор")
	}
	catID, err := strconv.ParseInt(catPart, 10, 32)
	if err != nil {
		return respond(c, "Не понял выбор")
	}

	tx, err := b.owned(ctx, c)
	if err != nil {
		return respond(c, err.Error())
	}

	if _, err := b.store.SetCategory(ctx, tx.ID, tx.PayerID, int32(catID)); err != nil {
		b.log.Error("смена категории", "err", err, "tx", tx.ID)
		return respond(c, "База не отвечает")
	}

	cats, err := b.store.Categories(ctx)
	if err != nil {
		b.log.Warn("не прочитал категории", "err", err)
	}
	id := int32(catID)
	tx.CategoryID = &id
	tx.CategoryName = categoryName(cats, id)
	classify.RememberManual(ctx, b.store, tx, b.log)
	// Флаг снимается только после того, как правка учтена: до этого места
	// tx.NeedsClassification говорит, что описание собрано из сырого текста.
	tx.NeedsClassification = false

	if err := b.edit(c, tx); err != nil {
		return err
	}
	return c.Respond()
}

// onDelete — удаление мягкое, сообщение остаётся как след (§9).
func (b *Bot) onDelete(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	tx, err := b.owned(ctx, c)
	if err != nil {
		return respond(c, err.Error())
	}

	if _, err := b.store.DeleteTransaction(ctx, tx.ID, tx.PayerID); err != nil {
		b.log.Error("удаление траты", "err", err, "tx", tx.ID)
		return respond(c, "База не отвечает")
	}

	if err := editMessage(c, "🗑 удалено", nil); err != nil {
		return err
	}
	return c.Respond()
}

// Ответы пользователю, когда правка невозможна. Различать «чужая» и «уже
// удалена» важно: иначе бот обвиняет человека в чужой трате на его же.
var (
	errNotFound = errors.New("Трата уже удалена")
	errNotYours = errors.New("Это не твоя трата")
	errBadData  = errors.New("Не понял, какая это трата")
)

// owned читает транзакцию из коллбэка и проверяет, что жал её плательщик (§9).
func (b *Bot) owned(ctx context.Context, c tele.Context) (storage.Transaction, error) {
	idPart, _, _ := strings.Cut(callbackData(c), ":")
	txID, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil {
		return storage.Transaction{}, errBadData
	}

	tx, err := b.store.Transaction(ctx, txID)
	if err != nil {
		return storage.Transaction{}, errNotFound
	}
	if tx.PayerID != c.Sender().ID {
		return storage.Transaction{}, errNotYours
	}
	return tx, nil
}

// edit переписывает исходное сообщение, а не шлёт новое (§9).
func (b *Bot) edit(c tele.Context, tx storage.Transaction) error {
	return editMessage(c, transactionLine(tx, time.Now(), b.cfg.TZ), keyboardFor(tx))
}

// editMessage правит сообщение, считая «текст не изменился» нормальным
// исходом: пользователь мог нажать кнопку, которая уже выбрана.
func editMessage(c tele.Context, text string, markup *tele.ReplyMarkup) error {
	var err error
	if markup == nil {
		err = c.Edit(text)
	} else {
		err = c.Edit(text, markup)
	}
	if errors.Is(err, tele.ErrSameMessageContent) {
		return nil
	}
	return err
}

// respond показывает пользователю всплывающее уведомление и закрывает
// «часики» на кнопке.
func respond(c tele.Context, text string) error {
	return c.Respond(&tele.CallbackResponse{Text: text})
}

// callbackData — полезная часть данных коллбэка без служебного префикса.
func callbackData(c tele.Context) string {
	return strings.TrimSpace(c.Callback().Data)
}
