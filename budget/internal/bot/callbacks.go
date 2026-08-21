package bot

import (
	"context"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"budget/internal/storage"
)

// deletedLine — что остаётся на месте траты после удаления.
const deletedLine = "✗ Удалено"

// onDelete убирает трату, на которую нажали.
//
// Группа берётся у того, кто нажал, а не из callback: подставить в кнопку
// чужой id траты может кто угодно, и хранилище, суженное до своей группы,
// такую запись просто не найдёт.
func (b *Bot) onDelete(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	group, id, ok := b.callbackTarget(ctx, c)
	if !ok {
		return nil
	}

	deleted, err := group.DeleteTransaction(ctx, id)
	if err != nil {
		b.log.Error("удаление траты", "err", err, "tx", id)
		return c.Respond(&tele.CallbackResponse{Text: "База не отвечает, попробуй ещё раз."})
	}
	if !deleted {
		// Записи нет, она чужая или её уже удалили — снаружи это одно и то же.
		return b.replaceWith(c, deletedLine, undoMarkup(id), "Уже удалено")
	}
	return b.replaceWith(c, deletedLine, undoMarkup(id), "Удалено")
}

// onRestore возвращает удалённую трату.
func (b *Bot) onRestore(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	group, id, ok := b.callbackTarget(ctx, c)
	if !ok {
		return nil
	}

	if _, err := group.RestoreTransaction(ctx, id); err != nil {
		b.log.Error("возврат траты", "err", err, "tx", id)
		return c.Respond(&tele.CallbackResponse{Text: "База не отвечает, попробуй ещё раз."})
	}

	tx, err := group.Transaction(ctx, id)
	if err != nil {
		b.log.Error("чтение возвращённой траты", "err", err, "tx", id)
		return c.Respond(&tele.CallbackResponse{Text: "Не нашёл эту трату."})
	}
	return b.replaceWith(c, transactionLine(tx, time.Now(), b.cfg.TZ), txMarkup(id), "Вернул")
}

// callbackTarget разбирает нажатие: чья группа и какая трата.
func (b *Bot) callbackTarget(ctx context.Context, c tele.Context) (*storage.GroupStore, int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Data()), 10, 64)
	if err != nil || id <= 0 {
		b.log.Warn("непонятная кнопка", "data", c.Data())
		_ = c.Respond(&tele.CallbackResponse{Text: "Не понял, какая это трата."})
		return nil, 0, false
	}

	member, inGroup, err := b.store.MemberOf(ctx, c.Sender().ID)
	if err != nil {
		b.log.Error("поиск группы", "err", err, "user_id", c.Sender().ID)
		_ = c.Respond(&tele.CallbackResponse{Text: "База не отвечает, попробуй ещё раз."})
		return nil, 0, false
	}
	if !inGroup {
		_ = c.Respond(&tele.CallbackResponse{Text: "Ты не в группе."})
		return nil, 0, false
	}
	return b.store.ForGroup(member.GroupID), id, true
}

// replaceWith переписывает сообщение под кнопкой и гасит «часики» у нажавшего.
//
// Ответить на callback надо в любом случае: без этого Telegram крутит
// индикатор ожидания секунд десять, и кнопка выглядит сломанной.
func (b *Bot) replaceWith(c tele.Context, text string, markup *tele.ReplyMarkup, toast string) error {
	if _, err := b.tb.Edit(c.Message(), text, markup); err != nil {
		// Сообщение могло устареть — сама операция уже прошла, и молчать
		// об этом нельзя, но и ошибкой обработчика это не является.
		b.log.Warn("не переписал сообщение под кнопкой", "err", err)
	}
	return c.Respond(&tele.CallbackResponse{Text: toast})
}
