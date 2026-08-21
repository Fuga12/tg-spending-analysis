package bot

import (
	"context"
	"errors"
	"fmt"
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

// onInviteAccept вводит нажавшего в группу, куда его позвали.
func (b *Bot) onInviteAccept(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	id, ok := b.callbackID(c)
	if !ok {
		return nil
	}

	// Принимает только тот, кого звали: id приглашения лежит в кнопке, а
	// кнопку можно переслать кому угодно. Проверка — в хранилище, одной
	// транзакцией вместе с вводом в группу.
	m, err := b.groups.Accept(ctx, c.Sender().ID, id)
	if err != nil {
		return b.replaceWith(c, inviteAnswer(err), nil, inviteToast(err))
	}

	b.log.Info("человек вошёл в группу", "user_id", m.UserID, "group", m.GroupID)
	markup := appMarkup(b.cfg.AppURL)
	text := "Теперь ты в группе. " + strings.TrimPrefix(greeting, "Привет! ")
	if markup != nil {
		text += appHint
	}
	return b.replaceWith(c, text, markup, "Принято")
}

// onInviteDecline отказывается от приглашения.
func (b *Bot) onInviteDecline(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	id, ok := b.callbackID(c)
	if !ok {
		return nil
	}

	if err := b.groups.Decline(ctx, c.Sender().ID, id); err != nil {
		return b.replaceWith(c, inviteAnswer(err), nil, inviteToast(err))
	}
	return b.replaceWith(c, "Приглашение отклонено.", nil, "Отказ")
}

// inviteAnswer объясняет, почему приглашение не сработало. Причин немного, и
// каждая означает разное: «уже в группе» лечится выходом, «просрочено» —
// новым приглашением, а «приглашения нет» чаще всего значит, что на кнопку
// нажали дважды.
func inviteAnswer(err error) string {
	switch {
	case errors.Is(err, storage.ErrNoInvite):
		return "Это приглашение уже неактуально."
	case errors.Is(err, storage.ErrInviteExpired):
		return "Приглашение просрочено — попроси позвать заново."
	case errors.Is(err, storage.ErrAlreadyMember):
		return "Ты уже состоишь в группе. Один человек ведёт один бюджет."
	case errors.Is(err, storage.ErrGroupFull):
		return "В группе больше нет мест."
	default:
		return "Не получилось, попробуй ещё раз."
	}
}

// inviteToast — та же причина коротко, всплывающей подсказкой.
func inviteToast(err error) string {
	switch {
	case errors.Is(err, storage.ErrInviteExpired):
		return "Просрочено"
	case errors.Is(err, storage.ErrAlreadyMember):
		return "Ты уже в группе"
	case errors.Is(err, storage.ErrGroupFull):
		return "Мест нет"
	case errors.Is(err, storage.ErrNoInvite):
		return "Неактуально"
	default:
		return "Не получилось"
	}
}

// InviteReceived пишет приглашённому, что его позвали.
//
// Через бота, а не только в приложении: человека, которого зовут, в группе
// ещё нет, и без сообщения он о приглашении просто не узнает.
func (b *Bot) InviteReceived(inv storage.Invite) error {
	text := fmt.Sprintf("%s зовёт тебя вести общий бюджет — «%s».",
		inv.InviterName, inv.GroupName)
	_, err := b.tb.Send(tele.ChatID(inv.InviteeUserID), text, inviteMarkup(inv.ID))
	return err
}

// callbackID достаёт из кнопки идентификатор, с которым она связана.
func (b *Bot) callbackID(c tele.Context) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Data()), 10, 64)
	if err != nil || id <= 0 {
		b.log.Warn("непонятная кнопка", "data", c.Data())
		_ = c.Respond(&tele.CallbackResponse{Text: "Не понял, о чём эта кнопка."})
		return 0, false
	}
	return id, true
}

// callbackTarget разбирает нажатие: чья группа и какая трата.
func (b *Bot) callbackTarget(ctx context.Context, c tele.Context) (*storage.GroupStore, int64, bool) {
	id, ok := b.callbackID(c)
	if !ok {
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
	if _, err := b.tb.Edit(c.Message(), text, editOptions(markup)...); err != nil {
		// Сообщение могло устареть — сама операция уже прошла, и молчать
		// об этом нельзя, но и ошибкой обработчика это не является.
		b.log.Warn("не переписал сообщение под кнопкой", "err", err)
	}
	return c.Respond(&tele.CallbackResponse{Text: toast})
}
