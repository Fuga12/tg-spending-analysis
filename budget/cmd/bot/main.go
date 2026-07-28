package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"budget/internal/bot"
	"budget/internal/classify"
	"budget/internal/config"
	"budget/internal/reminder"
	"budget/internal/storage"
	"budget/internal/worker"
)

// shutdownTimeout — сколько ждём уже начатые обработчики, прежде чем гасить
// клиента Telegram и пул БД.
const shutdownTimeout = 20 * time.Second

// backfillPeriod — как часто воркер добирает непроклассифицированные
// записи (§12).
const backfillPeriod = 10 * time.Minute

func main() {
	migrateOnly := flag.Bool("migrate", false, "накатить миграции и выйти")
	migrateDown := flag.Bool("migrate-down", false, "откатить одну миграцию и выйти")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Миграции — отдельный режим (make migrate-up/migrate-down, §12), а не
	// побочный эффект старта бота: под Restart=always неудачная миграция
	// превратилась бы в цикл падений.
	if *migrateOnly || *migrateDown {
		dsn, err := config.LoadDatabaseURL()
		if err != nil {
			log.Error("конфиг", "err", err)
			os.Exit(1)
		}
		migrate := storage.Migrate
		if *migrateDown {
			migrate = storage.MigrateDown
		}
		if err := migrate(ctx, dsn); err != nil {
			log.Error("миграции", "err", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Error("конфиг", "err", err)
		os.Exit(1)
	}

	store, err := storage.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("БД", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// Уведомление о 80% потолка уходит владельцу — первому id из whitelist (§7).
	// Бот к этому моменту ещё не создан, поэтому ссылка проставляется после.
	var notifyOwner func(string)
	llm := classify.NewYandex(classify.YandexConfig{
		BaseURL:  cfg.LLMBaseURL,
		APIKey:   cfg.YandexAPIKey,
		FolderID: cfg.YandexFolderID,
		Model:    cfg.LLMModel,
		Timeout:  cfg.LLMTimeout,
	}, store, log)
	breaker := classify.NewBreaker(cfg.LLMBreakerCooldown, log)
	budget := classify.NewBudget(cfg.LLMMonthlyTokenBudget, store,
		func(text string) { notifyOwner(text) }, log)
	classifier := classify.NewService(store, llm, breaker, budget, log)

	b, err := bot.New(cfg, bot.Deps{
		Store:      store,
		Classifier: classifier,
		Breaker:    breaker,
		Budget:     budget,
	}, log)
	if err != nil {
		log.Error("бот", "err", err)
		os.Exit(1)
	}
	notifyOwner = func(text string) {
		if err := b.Send(cfg.OwnerID(), text); err != nil {
			log.Error("не отправил уведомление владельцу", "err", err)
		}
	}

	// Воркер добора: раз в 10 минут подбирает записи, которым не досталось
	// категории (§12).
	backfill := worker.New(store, classifier, breaker, budget, log)
	go backfill.Run(ctx, backfillPeriod)

	rem := reminder.New(cfg, store, b, log)
	if err := rem.Start(); err != nil {
		log.Error("напоминание", "err", err)
		os.Exit(1)
	}

	go func() {
		<-ctx.Done()
		log.Info("останавливаюсь, доделываю начатое")
		rem.Stop()
		b.Shutdown(shutdownTimeout)
	}()

	log.Info("запущен", "users", len(cfg.AllowedUserIDs), "model", cfg.LLMModel)
	b.Start()
	log.Info("остановлен")
}
