package config

import (
	"testing"
)

func TestParseIDs(t *testing.T) {
	t.Run("список", func(t *testing.T) {
		ids, err := parseIDs(" 111 , 222 ,333 ")
		if err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
		if len(ids) != 3 || ids[0] != 111 || ids[2] != 333 {
			t.Errorf("ids = %v, ожидалось [111 222 333]", ids)
		}
	})

	t.Run("пусто — это открытый бот, а не ошибка", func(t *testing.T) {
		ids, err := parseIDs("")
		if err != nil {
			t.Fatalf("пустой список не должен быть ошибкой: %v", err)
		}
		if len(ids) != 0 {
			t.Errorf("ids = %v, ожидался пустой", ids)
		}
	})

	for _, c := range []struct{ name, in string }{
		{"не число", "111,абв"},
		{"дубль", "111,111"},
	} {
		t.Run("отказ: "+c.name, func(t *testing.T) {
			if _, err := parseIDs(c.in); err == nil {
				t.Errorf("%q не должно приниматься", c.in)
			}
		})
	}
}

func TestWhitelist(t *testing.T) {
	open := &Config{}
	if open.WhitelistEnabled() {
		t.Error("пустой список — whitelist выключен")
	}
	// Пока список пуст, бот открыт: кто с кем ведёт бюджет, решает группа.
	if !open.IsAllowed(777) {
		t.Error("при выключенном whitelist пускаем всех")
	}
	if open.OwnerID() != 0 {
		t.Error("без whitelist владельца нет")
	}

	closed := &Config{AllowedUserIDs: []int64{111, 222}}
	if !closed.WhitelistEnabled() {
		t.Error("непустой список — whitelist включён")
	}
	if !closed.IsAllowed(222) || closed.IsAllowed(333) {
		t.Error("пускаем только тех, кто в списке")
	}
	if closed.OwnerID() != 111 {
		t.Errorf("владелец = %d, ожидался первый id", closed.OwnerID())
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("BOT_TOKEN", "t")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("YANDEX_API_KEY", "k")
	t.Setenv("YANDEX_FOLDER_ID", "f")
	t.Setenv("ALLOWED_USER_IDS", "1,2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("конфиг: %v", err)
	}
	if cfg.OwnerID() != 1 {
		t.Errorf("владелец = %d, ожидался первый id из whitelist", cfg.OwnerID())
	}
	if cfg.LLMTimeout == 0 || cfg.LLMBreakerCooldown == 0 {
		t.Error("умолчания предохранителей не проставились")
	}
}

func TestParseHHMM(t *testing.T) {
	if _, _, err := ParseHHMM("21:00"); err != nil {
		t.Errorf("«21:00» должно разбираться: %v", err)
	}
	for _, bad := range []string{"25:00", "21:60", "21", "", "abc", "-1:00"} {
		if _, _, err := ParseHHMM(bad); err == nil {
			t.Errorf("%q не должно разбираться", bad)
		}
	}
}

func TestAppURL(t *testing.T) {
	base := func(t *testing.T) {
		t.Helper()
		t.Setenv("BOT_TOKEN", "t")
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("YANDEX_API_KEY", "k")
		t.Setenv("YANDEX_FOLDER_ID", "f")
	}

	t.Run("без APP_URL приложения просто нет", func(t *testing.T) {
		base(t)
		t.Setenv("APP_URL", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("конфиг: %v", err)
		}
		if cfg.HasApp() {
			t.Error("без APP_URL кнопок быть не должно")
		}
	})

	t.Run("хвостовой слэш срезается", func(t *testing.T) {
		base(t)
		t.Setenv("APP_URL", " https://budget.example.com/ ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("конфиг: %v", err)
		}
		if cfg.AppURL != "https://budget.example.com" || !cfg.HasApp() {
			t.Errorf("APP_URL = %q, ожидался без хвостового слэша", cfg.AppURL)
		}
	})

	// Telegram просто не откроет приложение по http, и кнопка будет молча
	// ничего не делать. Лучше не стартовать.
	for _, bad := range []string{"http://budget.example.com", "budget.example.com", "ftp://x"} {
		t.Run("отказ: "+bad, func(t *testing.T) {
			base(t)
			t.Setenv("APP_URL", bad)

			if _, err := Load(); err == nil {
				t.Errorf("%q не должно приниматься: Mini App работает только по https", bad)
			}
		})
	}
}
