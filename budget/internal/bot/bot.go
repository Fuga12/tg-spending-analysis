package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"budget/internal/classify"
	"budget/internal/config"
	"budget/internal/storage"
)

// Deps — всё, чем бот пользуется снаружи. Отдельной структурой, чтобы
// команда /лимит могла спросить состояние предохранителей (§7).
type Deps struct {
	Store      *storage.Store
	Classifier *classify.Service
	Breaker    *classify.Breaker
	Budget     *classify.Budget
}

// Bot — обёртка над telebot: long polling, whitelist, обработчики.
type Bot struct {
	tb         *tele.Bot
	cfg        *config.Config
	store      *storage.Store
	classifier *classify.Service
	breaker    *classify.Breaker
	budget     *classify.Budget
	log        *slog.Logger

	// inflight считает обработчики в работе: telebot запускает каждый в своей
	// горутине, и без этого счётчика остановка рвёт их на середине вместе с
	// пулом БД. Терять записи нельзя (§8).
	inflight sync.WaitGroup
}

// New собирает бота и регистрирует обработчики.
func New(cfg *config.Config, d Deps, log *slog.Logger) (*Bot, error) {
	tb, err := tele.NewBot(tele.Settings{
		Token:  cfg.BotToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		OnError: func(err error, c tele.Context) {
			log.Error("необработанная ошибка", "err", err)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}

	b := &Bot{
		tb:         tb,
		cfg:        cfg,
		store:      d.Store,
		classifier: d.Classifier,
		breaker:    d.Breaker,
		budget:     d.Budget,
		log:        log,
	}
	b.tb.Use(b.track, b.whitelist)
	b.routes()
	return b, nil
}

// Send отправляет сообщение вне контекста апдейта — служебные уведомления
// владельцу (§7) и напоминания (§12).
func (b *Bot) Send(userID int64, text string) error {
	_, err := b.tb.Send(tele.ChatID(userID), text)
	return err
}

// Start запускает long polling. Блокирует до Shutdown.
func (b *Bot) Start() { b.tb.Start() }

// Shutdown ждёт, пока договорят уже начатые обработчики, и только потом гасит
// клиента: Stop рвёт исходящие запросы к Telegram, так что порядок важен.
// Зависший обработчик не должен держать сервис вечно — отсюда потолок.
func (b *Bot) Shutdown(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		b.inflight.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		b.log.Warn("обработчики не уложились в таймаут остановки", "timeout", timeout)
	}
	b.tb.Stop()
}

// track — учёт обработчиков в работе, см. Bot.inflight.
func (b *Bot) track(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		b.inflight.Add(1)
		defer b.inflight.Done()
		return next(c)
	}
}

// whitelist — единственная авторизация в боте (§9).
func (b *Bot) whitelist(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		sender := c.Sender()
		if sender == nil || !b.cfg.IsAllowed(sender.ID) {
			var id int64
			if sender != nil {
				id = sender.ID
			}
			b.log.Warn("чужой пользователь", "user_id", id)
			return c.Send("Бот приватный.")
		}
		return next(c)
	}
}

// ctx — контекст запроса к БД с разумным потолком.
func (b *Bot) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// classifyCtx — отдельный контекст на разбор сообщения. Разбор может съесть
// две попытки по LLM_TIMEOUT плюс backoff, и делить дедлайн с записью в базу
// нельзя: иначе трата разобрана, а сохранить её уже нечем (§8).
func (b *Bot) classifyCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*b.cfg.LLMTimeout+10*time.Second)
}

func (b *Bot) routes() {
	b.tb.Handle("/start", b.onStart)

	// Команды продублированы латиницей (§9).
	for _, r := range []struct {
		ru, en  string
		handler tele.HandlerFunc
	}{
		{"/месяц", "/month", b.onMonth},
		{"/день", "/day", b.onDay},
		{"/лимит", "/usage", b.onUsage},
		{"/категории", "/categories", b.onCategories},
		{"/помощь", "/help", b.onHelp},
	} {
		b.tb.Handle(r.ru, r.handler)
		b.tb.Handle(r.en, r.handler)
	}

	b.tb.Handle(tele.OnText, b.onText)

	b.tb.Handle(&tele.Btn{Unique: cbPayer}, b.onBeneficiary(classify.BenPayer))
	b.tb.Handle(&tele.Btn{Unique: cbPartner}, b.onBeneficiary(classify.BenPartner))
	b.tb.Handle(&tele.Btn{Unique: cbBoth}, b.onBeneficiary(classify.BenBoth))
	b.tb.Handle(&tele.Btn{Unique: cbCategory}, b.onCategoryOpen)
	b.tb.Handle(&tele.Btn{Unique: cbPick}, b.onCategoryPick)
	b.tb.Handle(&tele.Btn{Unique: cbDelete}, b.onDelete)
}
