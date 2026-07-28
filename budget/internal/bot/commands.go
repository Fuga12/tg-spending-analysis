package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"budget/internal/auth"
	"budget/internal/report"
	"budget/internal/storage"
)

const helpText = `Пиши тратами, как говоришь:

  600 лимонад
  такси 450
  вчера взял в пятёрочке на 1200 и такси 400
  скинул ей 5к — это перевод, не трата

Под каждой записью кнопки: на кого потрачено, категория, удалить.
Правки запоминаются — в следующий раз то же слово разберётся само.

Команды
  /месяц — отчёт за текущий месяц
  /месяц 6 — отчёт за июнь
  /день — что потрачено сегодня
  /категории — список категорий
  /лимит — расход токенов
  /вход — ссылка на сайт: правки и графики
  /помощь — эта шпаргалка`

// onMonth — отчёт за календарный месяц, четыре блока (§10).
func (b *Bot) onMonth(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	now := time.Now().In(b.cfg.TZ)
	year, month := now.Year(), now.Month()

	if arg := strings.TrimSpace(c.Message().Payload); arg != "" {
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 12 {
			return c.Send("Месяц — число от 1 до 12. Например: /месяц 6")
		}
		month = time.Month(n)
	}

	from, to := report.MonthRange(year, month, b.cfg.TZ)
	rows, err := b.store.Expenses(ctx, from, to)
	if err != nil {
		b.log.Error("отчёт за месяц", "err", err)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}
	users, err := b.store.Users(ctx)
	if err != nil {
		b.log.Error("список пользователей", "err", err)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}

	return sendFixedWidth(c, formatMonth(report.BuildMonth(year, month, rows, users)))
}

// onDay — траты за сегодня, списком (§9).
func (b *Bot) onDay(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	now := time.Now()
	from, to := report.DayRange(now, b.cfg.TZ)
	txs, err := b.store.TransactionsBetween(ctx, from, to)
	if err != nil {
		b.log.Error("траты за день", "err", err)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}
	if len(txs) == 0 {
		return c.Send("Сегодня трат нет.")
	}

	users, err := b.store.Users(ctx)
	if err != nil {
		b.log.Warn("список пользователей", "err", err)
	}
	return sendFixedWidth(c, formatDay(txs, users, now, b.cfg.TZ))
}

// onUsage — расход токенов и состояние предохранителей (§7).
func (b *Bot) onUsage(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	used, err := b.budget.Used(ctx)
	if err != nil {
		b.log.Error("расход токенов", "err", err)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}
	errs, err := b.store.UsageErrors(ctx)
	if err != nil {
		b.log.Warn("разбивка ошибок", "err", err)
	}

	open, until := b.breaker.State()
	return c.Send(formatUsage(usageView{
		Used:        used,
		Errors:      errs,
		Limit:       b.budget.Limit(),
		PricePer1K:  b.cfg.LLMPricePer1KRub,
		Stats:       b.classifier.Stats(),
		BreakerOpen: open,
		BreakerTill: until.In(b.cfg.TZ),
	}))
}

// onCategories — список категорий (§9).
func (b *Bot) onCategories(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()

	cats, err := b.store.Categories(ctx)
	if err != nil {
		b.log.Error("категории", "err", err)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}

	users, err := b.store.Users(ctx)
	if err != nil {
		b.log.Warn("не прочитал участников", "err", err)
	}

	var sb strings.Builder
	sb.WriteString("Категории\n")
	for _, cat := range cats {
		fmt.Fprintf(&sb, "  %s · %s\n", cat.Name, categoryDefault(cat, users))
	}
	return c.Send(strings.TrimRight(sb.String(), "\n"))
}

func (b *Bot) onHelp(c tele.Context) error {
	return c.Send(helpText)
}

// usageView — всё, что нужно показать в /лимит.
type usageView struct {
	Used        storage.MonthUsage
	Errors      map[string]int
	Limit       int64
	PricePer1K  float64
	Stats       statsView
	BreakerOpen bool
	BreakerTill time.Time
}

// onLogin выдаёт одноразовую ссылку на веб-интерфейс (webapp.md §1).
func (b *Bot) onLogin(c tele.Context) error {
	if !b.cfg.WebEnabled() {
		return c.Send("Веб-интерфейс не настроен.")
	}
	// Ссылка даёт полный доступ к истории трат. В группе её увидят все —
	// и первый успевший войдёт под автором команды.
	if chat := c.Chat(); chat == nil || chat.Type != tele.ChatPrivate {
		return c.Send("Ссылку на сайт пришлю только в личку — напиши мне туда.")
	}

	ctx, cancel := b.ctx()
	defer cancel()

	sender := c.Sender()
	if err := b.store.UpsertUser(ctx, sender.ID, displayName(sender)); err != nil {
		b.log.Error("upsert пользователя", "err", err, "user_id", sender.ID)
		return c.Send("База не отвечает, попробуй ещё раз.")
	}

	token, hash, err := auth.NewToken()
	if err != nil {
		b.log.Error("токен входа", "err", err)
		return c.Send("Не смог выдать ссылку, попробуй ещё раз.")
	}
	if err := b.store.CreateLoginToken(ctx, hash, sender.ID, auth.LoginTokenTTL); err != nil {
		b.log.Error("запись токена входа", "err", err)
		return c.Send("Не смог выдать ссылку, попробуй ещё раз.")
	}

	b.log.Info("выдана ссылка входа", "user_id", sender.ID)
	// Ссылка одноразовая и живёт пять минут — предупреждаем прямо здесь,
	// чтобы её не сохраняли в закладки.
	// Без NoPreview Telegram сходит по ссылке сам, чтобы построить превью, —
	// и одноразовый токен сгорит до того, как по нему кликнут.
	return c.Send(auth.LoginURL(b.cfg.WebBaseURL, token)+
		"\n\nСсылка одна на один вход и живёт 5 минут. Нужна новая — снова /вход.",
		tele.NoPreview)
}
