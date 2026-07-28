package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"budget/internal/auth"
	"budget/internal/backup"
	"budget/internal/bot"
	"budget/internal/classify"
	"budget/internal/config"
	"budget/internal/reminder"
	"budget/internal/storage"
	"budget/internal/web"
	"budget/internal/worker"
)

// shutdownTimeout — сколько ждём уже начатые обработчики, прежде чем гасить
// клиента Telegram и пул БД.
// Худший случай одного обработчика — разбор (2×LLM_TIMEOUT + запас) плюс
// запись; таймаут должен быть заметно больше, иначе гарантия §8 не держится.
const shutdownTimeout = 45 * time.Second

// backfillPeriod — как часто воркер добирает непроклассифицированные
// записи (§12).
const backfillPeriod = 10 * time.Minute

func main() {
	migrateOnly := flag.Bool("migrate", false, "накатить миграции и выйти")
	migrateDown := flag.Bool("migrate-down", false, "откатить одну миграцию и выйти")
	webOnly := flag.Bool("web-only", false, "поднять только веб-интерфейс, без бота")
	loginLink := flag.Int64("login-link", 0, "выдать ссылку входа для telegram id и выйти")
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

	// Ссылка входа без бота: пока нет BOT_TOKEN, на сайт иначе не попасть.
	if *loginLink != 0 {
		printLoginLink(ctx, log, *loginLink)
		return
	}

	// Режим «только веб»: ни Telegram, ни модель не нужны. Нужен, чтобы
	// править фронт, не имея боевых токенов.
	if *webOnly {
		runWebOnly(ctx, log)
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
	backfillDone := make(chan struct{})
	go backfill.Run(ctx, backfillPeriod, backfillDone)

	// Веб поднимается, только если задан WEB_BASE_URL. Не задан — бот
	// работает как работал (webapp.md §3).
	var webSrv *web.Server
	if cfg.WebEnabled() {
		webSrv, err = web.New(cfg, store, log)
		if err != nil {
			log.Error("веб", "err", err)
			os.Exit(1)
		}
		if err := webSrv.Start(); err != nil {
			log.Error("веб", "err", err)
			os.Exit(1)
		}
	} else {
		log.Info("веб выключен: WEB_BASE_URL не задан")
	}

	rem := reminder.New(cfg, store, b, log)
	if err := rem.Start(); err != nil {
		log.Error("напоминание", "err", err)
		os.Exit(1)
	}

	// Сторож бэкапов: крон делает дампы, а кто-то должен заметить, если
	// перестал (§12).
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
		if webSrv != nil {
			webSrv.Shutdown()
		}
		rem.Stop()
		if backups != nil {
			backups.Stop()
		}
		b.Shutdown(shutdownTimeout)
	}()

	log.Info("запущен", "users", len(cfg.AllowedUserIDs), "model", cfg.LLMModel)
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

// runWebOnly поднимает интерфейс без бота и держит его до сигнала.
func runWebOnly(ctx context.Context, log *slog.Logger) {
	cfg, err := config.LoadWeb()
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

	srv, err := web.New(cfg, store, log)
	if err != nil {
		log.Error("веб", "err", err)
		os.Exit(1)
	}
	if err := srv.Start(); err != nil {
		log.Error("веб", "err", err)
		os.Exit(1)
	}
	log.Info("только веб, бот не запущен")

	<-ctx.Done()
	srv.Shutdown()
	log.Info("остановлен")
}

// printLoginLink печатает одноразовую ссылку входа. Нужна, пока бот не
// запущен: выдавать ссылки — его работа, но без токена Telegram его нет.
func printLoginLink(ctx context.Context, log *slog.Logger, userID int64) {
	cfg, err := config.LoadWeb()
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

	// Имя не трогаем: UpsertUser затёр бы настоящее имя партнёра на «Я»,
	// и отчёты бота стали бы врать.
	if err := store.EnsureUser(ctx, userID, "Я"); err != nil {
		log.Error("пользователь", "err", err)
		os.Exit(1)
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		log.Error("токен", "err", err)
		os.Exit(1)
	}
	if err := store.CreateLoginToken(ctx, hash, userID, auth.LoginTokenTTL); err != nil {
		log.Error("запись токена", "err", err)
		os.Exit(1)
	}
	fmt.Println(auth.LoginURL(cfg.WebBaseURL, token))
}
