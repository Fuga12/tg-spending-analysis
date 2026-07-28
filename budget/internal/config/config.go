package config

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — вся настройка бота. Заполняется из окружения, см. .env.example.
type Config struct {
	BotToken       string
	DatabaseURL    string
	AllowedUserIDs []int64
	TZ             *time.Location
	ReminderAt     string // "HH:MM"

	YandexAPIKey          string
	YandexFolderID        string
	LLMBaseURL            string
	LLMModel              string
	LLMTimeout            time.Duration
	LLMMonthlyTokenBudget int64
	LLMPricePer1KRub      float64
	LLMBreakerCooldown    time.Duration

	// Веб-интерфейс (webapp.md). Пустой WebBaseURL означает «веба нет»:
	// сервер не поднимается, бот работает как работал.
	WebAddr            string
	WebBaseURL         string
	WebInsecureCookies bool
}

// WebEnabled — настроен ли веб-интерфейс.
func (c *Config) WebEnabled() bool { return c.WebBaseURL != "" }

// OwnerID — первый id из whitelist, ему уходят служебные уведомления (§7).
func (c *Config) OwnerID() int64 {
	if len(c.AllowedUserIDs) == 0 {
		return 0
	}
	return c.AllowedUserIDs[0]
}

// IsAllowed — проверка whitelist. Другой авторизации нет и не нужно (§9).
func (c *Config) IsAllowed(id int64) bool {
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
		WebAddr:        envDefault("WEB_ADDR", "127.0.0.1:8081"),
		WebBaseURL:     strings.TrimRight(strings.TrimSpace(os.Getenv("WEB_BASE_URL")), "/"),
	}

	var missing []string
	for _, req := range []struct {
		name string
		val  string
	}{
		{"BOT_TOKEN", c.BotToken},
		{"DATABASE_URL", c.DatabaseURL},
		{"ALLOWED_USER_IDS", os.Getenv("ALLOWED_USER_IDS")},
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

	ids, err := parseIDs(os.Getenv("ALLOWED_USER_IDS"))
	if err != nil {
		return nil, fmt.Errorf("ALLOWED_USER_IDS: %w", err)
	}
	c.AllowedUserIDs = ids

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
	if c.LLMBreakerCooldown, err = envDuration("LLM_BREAKER_COOLDOWN", 30*time.Minute); err != nil {
		return nil, err
	}
	if c.LLMMonthlyTokenBudget, err = envInt64("LLM_MONTHLY_TOKEN_BUDGET", 2_000_000); err != nil {
		return nil, err
	}
	if c.LLMPricePer1KRub, err = envFloat("LLM_PRICE_PER_1K_RUB", 1.0); err != nil {
		return nil, err
	}

	c.WebInsecureCookies = envBool("WEB_INSECURE_COOKIES")
	if err := c.checkWeb(); err != nil {
		return nil, err
	}

	return c, nil
}

// checkWeb проверяет настройку веба до старта: ошибка здесь дешевле, чем
// неработающий вход, в котором виноватым выглядит бот.
func (c *Config) checkWeb() error {
	if !c.WebEnabled() {
		return nil
	}

	// Адрес без схемы Telegram не сделает ссылкой, и вход не заработает —
	// поэтому и неразбираемый адрес, и адрес без схемы дают одну подсказку.
	u, err := url.Parse(c.WebBaseURL)
	if err != nil {
		return fmt.Errorf("WEB_BASE_URL должен начинаться с http:// или https:// (%w)", err)
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return errors.New("WEB_BASE_URL должен начинаться с http:// или https://")
	}
	if u.Host == "" {
		return errors.New("WEB_BASE_URL: не разобрать адрес")
	}
	if !strings.EqualFold(u.Scheme, "https") && !c.WebInsecureCookies {
		// Без TLS браузер выбросит cookie с флагом Secure, и вход будет
		// молча не работать. Пусть это будет осознанным выбором.
		return errors.New("WEB_BASE_URL без https требует WEB_INSECURE_COOKIES=1")
	}
	return nil
}

// LoadWeb — конфиг для режима «только веб»: ни токена бота, ни ключа Яндекса
// он не требует. Нужен, чтобы поднимать интерфейс отдельно от бота, когда
// правится фронт.
func LoadWeb() (*Config, error) {
	loadDotEnv(".env")

	c := &Config{
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		WebAddr:            envDefault("WEB_ADDR", "127.0.0.1:8081"),
		WebBaseURL:         strings.TrimRight(strings.TrimSpace(os.Getenv("WEB_BASE_URL")), "/"),
		WebInsecureCookies: envBool("WEB_INSECURE_COOKIES"),
	}
	if c.DatabaseURL == "" {
		return nil, errors.New("не задана переменная окружения DATABASE_URL")
	}
	if !c.WebEnabled() {
		return nil, errors.New("не задана переменная окружения WEB_BASE_URL")
	}
	if err := c.checkWeb(); err != nil {
		return nil, err
	}

	loc, err := time.LoadLocation(envDefault("TZ", "Europe/Moscow"))
	if err != nil {
		return nil, fmt.Errorf("TZ: %w", err)
	}
	c.TZ = loc

	if ids := strings.TrimSpace(os.Getenv("ALLOWED_USER_IDS")); ids != "" {
		if c.AllowedUserIDs, err = parseIDs(ids); err != nil {
			return nil, fmt.Errorf("ALLOWED_USER_IDS: %w", err)
		}
	}
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

func parseIDs(s string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q не является telegram id", part)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("список пуст")
	}
	return ids, nil
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

// envBool — «1», «true», «yes» означают да, всё остальное нет.
func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "да":
		return true
	}
	return false
}
