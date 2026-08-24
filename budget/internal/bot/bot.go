package bot

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"budget/internal/classify"
	"budget/internal/config"
	"budget/internal/group"
	"budget/internal/storage"
)

// Deps — всё, чем бот пользуется снаружи. Отдельной структурой, чтобы
// команда /лимит могла спросить состояние предохранителей.
type Deps struct {
	Store      *storage.Store
	Groups     *group.Service
	Classifier *classify.Service
	Breaker    *classify.Breaker
	Budget     *classify.Budget
}

// pollTimeout — сколько Telegram держит запрос getUpdates, пока нет новых
// сообщений. Это ожидание на его стороне, а не таймаут нашего клиента.
const pollTimeout = 10 * time.Second

// httpTimeout — потолок на один запрос к Telegram.
//
// Без него бот однажды замолкает навсегда, оставаясь живым и здоровым на вид.
// У клиента telebot таймаута нет по умолчанию, а getUpdates висит на соединении
// минутами: стоит промежуточному NAT тихо забыть про него — и запрос не
// вернётся никогда. Ни ошибки, ни строчки в логе, процесс active, перезапусков
// ноль, и только очередь сообщений копится на стороне Telegram.
//
// Обязан быть заметно больше pollTimeout, иначе рвался бы каждый нормальный
// долгий опрос.
const httpTimeout = 60 * time.Second

// telegramClient — HTTP-клиент с настоящими таймаутами на каждом шаге.
// Общий Timeout спасает от повисшего соединения, остальные — от зависания
// раньше и точнее.
func telegramClient() *http.Client {
	return &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
				// Мёртвое соединение обнаруживается пробами, а не по факту
				// вечного ожидания ответа.
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			// Больше pollTimeout: Telegram молчит ровно столько, пока копит
			// сообщения, и это не повод обрывать запрос.
			ResponseHeaderTimeout: pollTimeout + 30*time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// Bot — обёртка над telebot: long polling, whitelist, обработчики.
type Bot struct {
	tb         *tele.Bot
	cfg        *config.Config
	store      *storage.Store
	groups     *group.Service
	classifier *classify.Service
	breaker    *classify.Breaker
	budget     *classify.Budget
	log        *slog.Logger

	// commands — свой указатель команд, см. routes.
	commands map[string]tele.HandlerFunc

	// inflight считает обработчики в работе: telebot запускает каждый в своей
	// горутине, и без этого счётчика остановка рвёт их на середине вместе с
	// пулом БД. Терять записи нельзя.
	inflight sync.WaitGroup

	// limiter — потолок сообщений в сутки на человека. Пока бот открыт, один
	// увлёкшийся человек иначе выбирает грант за вечер.
	limiter *limiter
}

// New собирает бота и регистрирует обработчики.
func New(cfg *config.Config, d Deps, log *slog.Logger) (*Bot, error) {
	tb, err := tele.NewBot(tele.Settings{
		Token:  cfg.BotToken,
		Client: telegramClient(),
		Poller: &tele.LongPoller{Timeout: pollTimeout},
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
		groups:     d.Groups,
		classifier: d.Classifier,
		breaker:    d.Breaker,
		budget:     d.Budget,
		log:        log,
		limiter:    newLimiter(cfg.MessagesPerDay, cfg.TZ),
	}
	b.tb.Use(b.track, b.whitelist)
	b.routes()
	return b, nil
}

// Send отправляет сообщение вне контекста апдейта — служебные уведомления
// владельцу и напоминания.
func (b *Bot) Send(userID int64, text string) error {
	_, err := b.tb.Send(tele.ChatID(userID), text)
	return err
}

// Start запускает long polling. Блокирует до Shutdown.
func (b *Bot) Start() {
	b.setupMenuButton()
	b.tb.Start()
}

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

// whitelist — единственная авторизация в боте.
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
// нельзя: иначе трата разобрана, а сохранить её уже нечем.
func (b *Bot) classifyCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*b.cfg.LLMTimeout+10*time.Second)
}

// setupMenuButton вешает Mini App на кнопку меню рядом с полем ввода.
//
// Ставится один раз при старте и на всех приватных чатов сразу — поэтому
// Raw, а не SetMenuButton: тот требует конкретного собеседника, а нам нужно
// умолчание для всех. Неудача не должна мешать боту работать: без кнопки он
// по-прежнему записывает траты.
func (b *Bot) setupMenuButton() {
	if !b.cfg.HasApp() {
		b.log.Info("кнопка меню не ставится: APP_URL не задан")
		return
	}
	_, err := b.tb.Raw("setChatMenuButton", map[string]any{
		"menu_button": tele.MenuButton{
			Type:   tele.MenuButtonWebApp,
			Text:   "Бюджет",
			WebApp: &tele.WebApp{URL: b.cfg.AppURL},
		},
	})
	if err != nil {
		b.log.Error("не поставил кнопку меню", "err", err)
		return
	}
	b.log.Info("кнопка меню ведёт в приложение", "url", b.cfg.AppURL)
}

func (b *Bot) routes() {
	// Две команды и текст — вся поверхность бота. Всё, что можно показать
	// интерфейсом, живёт в приложении.
	commands := []struct {
		names   []string
		handler tele.HandlerFunc
	}{
		{[]string{"/start"}, b.onStart},
		{[]string{"/помощь", "/help"}, b.onHelp},
	}

	// telebot разбирает команды регуляркой с \w, под которую кириллица не
	// подходит: «/помощь» до обработчика не доезжает и попадает в OnText,
	// где записалось бы тратой. Поэтому держим свой указатель команд
	// и разбираем такие сообщения сами — см. dispatchCommand.
	b.commands = make(map[string]tele.HandlerFunc, 2*len(commands))
	for _, cmd := range commands {
		for _, name := range cmd.names {
			b.tb.Handle(name, cmd.handler)
			b.commands[name] = cmd.handler
		}
	}

	b.tb.Handle(btnDelete, b.onDelete)
	b.tb.Handle(btnRestore, b.onRestore)
	b.tb.Handle(btnInviteAccept, b.onInviteAccept)
	b.tb.Handle(btnInviteDecline, b.onInviteDecline)
	b.tb.Handle(tele.OnText, b.onText)
}
