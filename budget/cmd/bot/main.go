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
	"budget/internal/config"
	"budget/internal/storage"
)

// shutdownTimeout — сколько ждём уже начатые обработчики, прежде чем гасить
// клиента Telegram и пул БД.
const shutdownTimeout = 20 * time.Second

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

	b, err := bot.New(cfg, store, log)
	if err != nil {
		log.Error("бот", "err", err)
		os.Exit(1)
	}

	go func() {
		<-ctx.Done()
		log.Info("останавливаюсь, доделываю начатое")
		b.Shutdown(shutdownTimeout)
	}()

	log.Info("запущен", "users", len(cfg.AllowedUserIDs), "model", cfg.LLMModel)
	b.Start()
	log.Info("остановлен")
}
