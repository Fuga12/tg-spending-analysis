package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"budget/internal/config"
	"budget/internal/storage"
)

// Bot — обёртка над telebot: long polling, whitelist, обработчики.
type Bot struct {
	tb    *tele.Bot
	cfg   *config.Config
	store *storage.Store
	log   *slog.Logger

	// inflight считает обработчики в работе: telebot запускает каждый в своей
	// горутине, и без этого счётчика остановка рвёт их на середине вместе с
	// пулом БД. Терять записи нельзя (§8).
	inflight sync.WaitGroup
}

// New собирает бота и регистрирует обработчики.
func New(cfg *config.Config, store *storage.Store, log *slog.Logger) (*Bot, error) {
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

	b := &Bot{tb: tb, cfg: cfg, store: store, log: log}
	b.tb.Use(b.track, b.whitelist)
	b.routes()
	return b, nil
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

func (b *Bot) routes() {
	b.tb.Handle("/start", b.onStart)
}
