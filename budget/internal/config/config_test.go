package config

import (
	"strings"
	"testing"
)

func TestCheckWeb(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		wantErr  bool
		errMatch string
	}{
		{
			name: "веб выключен",
			cfg:  Config{},
		},
		{
			name: "https без оговорок",
			cfg:  Config{WebBaseURL: "https://budget.example.com"},
		},
		{
			name:     "http без явного разрешения",
			cfg:      Config{WebBaseURL: "http://84.252.135.13:8081"},
			wantErr:  true,
			errMatch: "WEB_INSECURE_COOKIES",
		},
		{
			name: "http с явным разрешением",
			cfg:  Config{WebBaseURL: "http://84.252.135.13:8081", WebInsecureCookies: true},
		},
		{
			name:     "адрес без схемы",
			cfg:      Config{WebBaseURL: "84.252.135.13:8081", WebInsecureCookies: true},
			wantErr:  true,
			errMatch: "http://",
		},
		{
			name: "схема в верхнем регистре",
			cfg:  Config{WebBaseURL: "HTTPS://budget.example.com"},
		},
		{
			name:     "схема не та",
			cfg:      Config{WebBaseURL: "ftp://budget.example.com", WebInsecureCookies: true},
			wantErr:  true,
			errMatch: "http://",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.checkWeb()
			if c.wantErr && err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if err != nil && c.errMatch != "" && !strings.Contains(err.Error(), c.errMatch) {
				t.Errorf("ошибка = %q, ожидалось упоминание %q", err, c.errMatch)
			}
		})
	}
}

func TestWebDefaults(t *testing.T) {
	t.Setenv("BOT_TOKEN", "t")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("ALLOWED_USER_IDS", "1,2")
	t.Setenv("YANDEX_API_KEY", "k")
	t.Setenv("YANDEX_FOLDER_ID", "f")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("конфиг: %v", err)
	}
	if cfg.WebAddr != "127.0.0.1:8081" {
		t.Errorf("WEB_ADDR по умолчанию = %q, ожидался 127.0.0.1:8081", cfg.WebAddr)
	}
	if cfg.WebEnabled() {
		t.Error("без WEB_BASE_URL веб должен быть выключен")
	}
	if cfg.OwnerID() != 1 {
		t.Errorf("владелец = %d, ожидался первый id из whitelist", cfg.OwnerID())
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
