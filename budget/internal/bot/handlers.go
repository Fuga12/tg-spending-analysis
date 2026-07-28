package bot

import (
	"strings"

	tele "gopkg.in/telebot.v3"
)

const greeting = `Привет! Я веду наш общий бюджет.

Просто пиши тратами, как говоришь:
  600 лимонад
  такси 450
  вчера взял в пятёрочке на 1200 и такси 400

Команды: /месяц, /день, /лимит, /категории, /помощь`

func (b *Bot) onStart(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	if err := b.store.UpsertUser(ctx, c.Sender().ID, displayName(c.Sender())); err != nil {
		b.log.Error("upsert пользователя", "err", err, "user_id", c.Sender().ID)
		return c.Send("Не смог записать тебя в базу, попробуй ещё раз.")
	}
	return c.Send(greeting)
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
