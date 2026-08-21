package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"budget/internal/backup"
	"budget/internal/bot"
	"budget/internal/classify"
	"budget/internal/config"
	"budget/internal/reminder"
	"budget/internal/storage"
	"budget/internal/worker"
)

// shutdownTimeout — сколько ждём уже начатые обработчики, прежде чем гасить
// клиента Telegram и пул БД.
// Худший случай одного обработчика — разбор (2×LLM_TIMEOUT + запас) плюс
// запись; таймаут должен быть заметно больше, иначе гарантия §8 не держится.
const shutdownTimeout = 45 * time.Second

// backfillPeriod — как часто воркер добирает непроклассифицированные записи.
const backfillPeriod = 10 * time.Minute

// botRetryPeriod — пауза между попытками подключиться к Telegram.
const botRetryPeriod = 30 * time.Second

func main() {
	migrateOnly := flag.Bool("migrate", false, "накатить миграции и выйти")
	migrateDown := flag.Bool("migrate-down", false, "откатить одну миграцию и выйти")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Миграции — отдельный режим (make migrate-up/migrate-down), а не побочный
	// эффект старта бота: под Restart=always неудачная миграция превратилась
	// бы в цикл падений.
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

	// Уведомление о 80% потолка уходит владельцу. Бот к этому моменту ещё
	// не создан, поэтому ссылка проставляется после.
	var notifyOwner func(string)
	llm := classify.NewYandex(classify.YandexConfig{
		BaseURL:  cfg.LLMBaseURL,
		APIKey:   cfg.YandexAPIKey,
		FolderID: cfg.YandexFolderID,
		Model:    cfg.LLMModel,
		Timeout:  cfg.LLMTimeout,
	}, log)
	breaker := classify.NewBreaker(cfg.LLMBreakerCooldown, log)
	budget := classify.NewBudget(cfg.LLMMonthlyTokenBudget, store,
		func(text string) { notifyOwner(text) }, log)
	classifier := classify.NewService(llm, breaker, budget, log)

	var b *bot.Bot
	for {
		b, err = bot.New(cfg, bot.Deps{
			Store:      store,
			Classifier: classifier,
			Breaker:    breaker,
			Budget:     budget,
		}, log)
		if err == nil {
			break
		}
		// Ошибка telebot содержит URL с токеном. Не пишем её в лог целиком.
		log.Error("Telegram недоступен, повторю подключение", "через", botRetryPeriod)
		select {
		case <-ctx.Done():
			return
		case <-time.After(botRetryPeriod):
		}
	}
	notifyOwner = func(text string) {
		if cfg.OwnerID() == 0 {
			log.Warn("некому отправить служебное уведомление", "текст", text)
			return
		}
		if err := b.Send(cfg.OwnerID(), text); err != nil {
			log.Error("не отправил уведомление владельцу", "err", err)
		}
	}

	// Воркер добора: раз в 10 минут подбирает записи, которым не досталось
	// категории.
	backfill := worker.New(store, classifier, breaker, budget, log)
	backfillDone := make(chan struct{})
	go backfill.Run(ctx, backfillPeriod, backfillDone)

	rem := reminder.New(cfg, store, b, log)
	if err := rem.Start(); err != nil {
		log.Error("напоминание", "err", err)
		os.Exit(1)
	}

	// Сторож бэкапов: крон делает дампы, а кто-то должен заметить, если
	// перестал.
	var backups *backup.Watcher
	if cfg.BackupDir != "" {
		backups = backup.New(cfg.BackupDir, cfg.BackupMaxAge, cfg.OwnerID(), cfg.TZ, b, log)
		if err := backups.Start(); err != nil {
			log.Error("слежение за бэкапами", "err", err)
			os.Exit(1)
		}
	} else {
		log.Info("слежение за бэкапами выключено: BACKUP_DIR не задан")
	}

	go func() {
		<-ctx.Done()
		log.Info("останавливаюсь, доделываю начатое")
		rem.Stop()
		if backups != nil {
			backups.Stop()
		}
		b.Shutdown(shutdownTimeout)
	}()

	log.Info("запущен", "whitelist", cfg.WhitelistEnabled(), "model", cfg.LLMModel)
	b.Start()

	// Пул БД закрывается отложенным store.Close — дождаться воркера надо
	// раньше, иначе начатый тик допишется в уже закрытый пул.
	select {
	case <-backfillDone:
	case <-time.After(shutdownTimeout):
		log.Warn("воркер добора не уложился в таймаут остановки")
	}
	log.Info("остановлен")
}
