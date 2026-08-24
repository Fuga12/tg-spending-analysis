package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — вся настройка бота. Заполняется из окружения, см. .env.example.
type Config struct {
	BotToken    string
	DatabaseURL string
	// AllowedUserIDs — кого вообще пускать в бота. Пустой список означает,
	// что бот открыт: кто с кем ведёт бюджет, решает состав группы, а не эта
	// настройка.
	AllowedUserIDs []int64
	// Owner — кому уходят служебные уведомления. Отдельной переменной, а не
	// первым из whitelist: при открытом боте whitelist пуст, и владельца
	// не оказывалось вовсе — предупреждение о потолке уходило в никуда.
	Owner      int64
	TZ         *time.Location
	ReminderAt string // "HH:MM"

	// AppURL — адрес Mini App. Пустой означает, что приложения ещё нет:
	// бот тогда не показывает ни кнопку под приветствием, ни кнопку меню.
	// Telegram открывает Mini App только по https, поэтому другой схемы тут
	// быть не может.
	AppURL string

	// AppListen — где слушает сервер приложения. Пусто — сервер не поднимается.
	AppListen string

	// AppDevUserID — от чьего имени пускать без подписи Telegram.
	//
	// Не удобство, а необходимость: браузерной ветки нет, и без байпаса
	// приложение нельзя открыть иначе как из Telegram, то есть нельзя
	// разрабатывать. Ровно поэтому он и опасен — см. громкий лог в app.New.
	AppDevUserID int64

	YandexAPIKey          string
	YandexFolderID        string
	LLMBaseURL            string
	LLMModel              string
	LLMTimeout            time.Duration
	LLMMonthlyTokenBudget int64
	// LLMGroupTokenBudget — месячный потолок на одну группу. Ноль выключает
	// его: до открытия бота считать по группам незачем.
	LLMGroupTokenBudget int64
	// MessagesPerDay — сколько сообщений в сутки бот разбирает от одного
	// человека. Ноль выключает счёт.
	MessagesPerDay     int
	LLMPricePer1KRub   float64
	LLMBreakerCooldown time.Duration

	// Слежение за бэкапами. Пустой BackupDir выключает проверку — так удобнее
	// локально, где бэкапов и не бывает.
	BackupDir    string
	BackupMaxAge time.Duration
}

// WhitelistEnabled — ограничен ли доступ списком. Пустой список означает
// открытого бота.
func (c *Config) WhitelistEnabled() bool { return len(c.AllowedUserIDs) > 0 }

// AppEnabled — поднимать ли сервер приложения.
func (c *Config) AppEnabled() bool { return c.AppListen != "" }

// DevBypass — пускают ли в приложение без подписи Telegram.
func (c *Config) DevBypass() bool { return c.AppDevUserID != 0 }

// HasApp — есть ли куда вести из бота. Пока приложения нет, кнопки не
// показываются: кнопка, ведущая в никуда, хуже её отсутствия.
func (c *Config) HasApp() bool { return c.AppURL != "" }

// OwnerID — кому уходят служебные уведомления. Ноль означает, что владелец
// не задан и отправлять их некому.
//
// OWNER_ID приоритетнее whitelist: список отвечает на вопрос «пускать ли
// вообще», и совпадение первого id с владельцем было совпадением, а не
// правилом.
func (c *Config) OwnerID() int64 {
	if c.Owner != 0 {
		return c.Owner
	}
	if len(c.AllowedUserIDs) == 0 {
		return 0
	}
	return c.AllowedUserIDs[0]
}

// IsAllowed — проверка whitelist. Пока список пуст, пускаем всех.
func (c *Config) IsAllowed(id int64) bool {
	if !c.WhitelistEnabled() {
		return true
	}
	for _, allowed := range c.AllowedUserIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

// Load читает конфиг из окружения. Перед этим подхватывает .env из текущего
// каталога, если он есть: под systemd используется EnvironmentFile, а локально
// удобнее файл. Уже выставленные переменные окружения приоритетнее файла.
func Load() (*Config, error) {
	loadDotEnv(".env")

	c := &Config{
		BotToken:       os.Getenv("BOT_TOKEN"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		YandexAPIKey:   os.Getenv("YANDEX_API_KEY"),
		YandexFolderID: os.Getenv("YANDEX_FOLDER_ID"),
		LLMBaseURL:     envDefault("LLM_BASE_URL", "https://ai.api.cloud.yandex.net/v1"),
		LLMModel:       envDefault("LLM_MODEL", "yandexgpt/rc"),
		ReminderAt:     envDefault("REMINDER_AT", "21:00"),
		BackupDir:      os.Getenv("BACKUP_DIR"),
		AppURL:         strings.TrimRight(strings.TrimSpace(os.Getenv("APP_URL")), "/"),
		AppListen:      strings.TrimSpace(os.Getenv("APP_LISTEN")),
	}

	// Ошибиться схемой легко, а последствие немое: Telegram просто не откроет
	// приложение, и кнопка будет молча ничего не делать.
	if c.AppURL != "" && !strings.HasPrefix(c.AppURL, "https://") {
		return nil, fmt.Errorf("APP_URL: Telegram открывает Mini App только по https, получено %q", c.AppURL)
	}

	var missing []string
	for _, req := range []struct {
		name string
		val  string
	}{
		{"BOT_TOKEN", c.BotToken},
		{"DATABASE_URL", c.DatabaseURL},
		{"YANDEX_API_KEY", c.YandexAPIKey},
		{"YANDEX_FOLDER_ID", c.YandexFolderID},
	} {
		if strings.TrimSpace(req.val) == "" {
			missing = append(missing, req.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("не заданы обязательные переменные окружения: %s", strings.Join(missing, ", "))
	}

	allowed, err := parseIDs(os.Getenv("ALLOWED_USER_IDS"))
	if err != nil {
		return nil, fmt.Errorf("ALLOWED_USER_IDS: %w", err)
	}
	c.AllowedUserIDs = allowed

	if c.AppDevUserID, err = envInt64Optional("APP_DEV_USER_ID"); err != nil {
		return nil, err
	}
	if c.Owner, err = envInt64Optional("OWNER_ID"); err != nil {
		return nil, err
	}

	loc, err := time.LoadLocation(envDefault("TZ", "Europe/Moscow"))
	if err != nil {
		return nil, fmt.Errorf("TZ: %w", err)
	}
	c.TZ = loc

	if _, _, err := ParseHHMM(c.ReminderAt); err != nil {
		return nil, fmt.Errorf("REMINDER_AT: %w", err)
	}

	if c.LLMTimeout, err = envDuration("LLM_TIMEOUT", 6*time.Second); err != nil {
		return nil, err
	}
	// 36 часов: суточный крон, плюс запас на сдвиг и на разовый сбой,
	// который сам себя лечит следующей ночью.
	if c.BackupMaxAge, err = envDuration("BACKUP_MAX_AGE", 36*time.Hour); err != nil {
		return nil, err
	}
	if c.LLMBreakerCooldown, err = envDuration("LLM_BREAKER_COOLDOWN", 30*time.Minute); err != nil {
		return nil, err
	}
	if c.LLMMonthlyTokenBudget, err = envInt64("LLM_MONTHLY_TOKEN_BUDGET", 2_000_000); err != nil {
		return nil, err
	}
	if c.LLMPricePer1KRub, err = envFloat("LLM_PRICE_PER_1K_RUB", 1.0); err != nil {
		return nil, err
	}
	if c.LLMGroupTokenBudget, err = envInt64Default("LLM_GROUP_TOKEN_BUDGET", 200_000); err != nil {
		return nil, err
	}
	messages, err := envInt64Default("MESSAGES_PER_DAY", 200)
	if err != nil {
		return nil, err
	}
	c.MessagesPerDay = int(messages)

	return c, nil
}

// LoadDatabaseURL достаёт только строку подключения: миграциям остальной конфиг
// не нужен, и требовать ради них токен бота и ключ Яндекса незачем.
func LoadDatabaseURL() (string, error) {
	loadDotEnv(".env")
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return "", errors.New("не задана переменная окружения DATABASE_URL")
	}
	return dsn, nil
}

// ParseHHMM разбирает "21:00" в часы и минуты.
func ParseHHMM(s string) (hour, min int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("ожидается формат HH:MM")
	}
	if hour, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, errors.New("ожидается формат HH:MM")
	}
	if min, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, errors.New("ожидается формат HH:MM")
	}
	if hour < 0 || hour > 23 || min < 0 || min > 59 {
		return 0, 0, errors.New("время вне диапазона")
	}
	return hour, min, nil
}

// parseIDs разбирает список telegram id через запятую. Пустой список —
// не ошибка: он означает, что бот открыт для всех, кто его найдёт.
//
// Числа людей здесь больше не проверяем: кто с кем ведёт бюджет, решает
// состав группы, а whitelist отвечает только на вопрос «пускать ли вообще».
func parseIDs(s string) ([]int64, error) {
	var out []int64
	seen := map[int64]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q не является telegram id", part)
		}
		if seen[id] {
			return nil, fmt.Errorf("id %d указан дважды", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func envDefault(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func envDuration(name string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: должно быть положительным", name)
	}
	return d, nil
}

// envInt64Optional — необязательное число. Пусто означает «выключено», а не
// ноль по умолчанию: разница здесь смысловая.
func envInt64Optional(name string) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s: ожидался telegram id, получено %q", name, raw)
	}
	return v, nil
}

// envInt64Default — число с умолчанием, где ноль означает «выключено» и
// потому допустим.
func envInt64Default(name string, def int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s: ожидалось неотрицательное число, получено %q", name, raw)
	}
	return v, nil
}

func envInt64(name string, def int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s: должно быть положительным", name)
	}
	return v, nil
}

func envFloat(name string, def float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("%s: не может быть отрицательным", name)
	}
	return v, nil
}

// loadDotEnv — минимальный разбор .env: KEY=VALUE, строки с # игнорируются,
// кавычки вокруг значения снимаются. Отсутствие файла — не ошибка.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}
