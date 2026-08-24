package app

import (
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testToken = "123456:AAHtestbottoken"

// signInitData собирает подписанный initData так же, как это делает Telegram.
// Собственная реализация подписи в тесте — не дублирование: она проверяет,
// что мы поняли алгоритм, а не что мы согласны сами с собой.
func signInitData(t *testing.T, token string, fields map[string]string) string {
	t.Helper()

	pairs := make([]string, 0, len(fields))
	for k, v := range fields {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)

	secret := hmacSHA256([]byte("WebAppData"), token)
	hash := hex.EncodeToString(hmacSHA256(secret, strings.Join(pairs, "\n")))

	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	q.Set("hash", hash)
	return q.Encode()
}

func validFields(now time.Time) map[string]string {
	return map[string]string{
		"auth_date":   strconv.FormatInt(now.Unix(), 10),
		"query_id":    "AAHdF6IQAAAAAN0Xoh",
		"user":        `{"id":777,"first_name":"Илья","last_name":"Петров","username":"ilya"}`,
		"chat_type":   "private",
		"start_param": "",
	}
}

func TestVerifyInitDataAcceptsGenuineSignature(t *testing.T) {
	now := time.Now()
	data := signInitData(t, testToken, validFields(now))

	user, err := VerifyInitData(data, testToken, now)
	if err != nil {
		t.Fatalf("настоящая подпись должна приниматься: %v", err)
	}
	if user.ID != 777 || user.DisplayName() != "Илья Петров" {
		t.Errorf("пользователь = %+v, ожидался Илья Петров (777)", user)
	}
}

func TestVerifyInitDataRejectsForgery(t *testing.T) {
	now := time.Now()

	t.Run("подпись от другого бота", func(t *testing.T) {
		data := signInitData(t, "999999:AAHotherbottoken", validFields(now))
		if _, err := VerifyInitData(data, testToken, now); !errors.Is(err, ErrBadSignature) {
			t.Errorf("ошибка = %v, ожидалось ErrBadSignature", err)
		}
	})

	t.Run("подменённый пользователь", func(t *testing.T) {
		// Самая опасная подделка: подпись настоящая, но id чужой. Ровно её
		// и ловит проверочная строка, куда user входит целиком.
		data := signInitData(t, testToken, validFields(now))
		forged := strings.Replace(data, url.QueryEscape(`"id":777`), url.QueryEscape(`"id":778`), 1)
		if forged == data {
			t.Fatal("подмена не удалась — тест ничего не проверяет")
		}
		if _, err := VerifyInitData(forged, testToken, now); !errors.Is(err, ErrBadSignature) {
			t.Errorf("ошибка = %v, ожидалось ErrBadSignature", err)
		}
	})

	t.Run("подменённая дата", func(t *testing.T) {
		fields := validFields(now)
		data := signInitData(t, testToken, fields)
		forged := strings.Replace(data,
			"auth_date="+fields["auth_date"],
			"auth_date="+strconv.FormatInt(now.Add(time.Hour).Unix(), 10), 1)
		if _, err := VerifyInitData(forged, testToken, now); !errors.Is(err, ErrBadSignature) {
			t.Errorf("ошибка = %v, ожидалось ErrBadSignature", err)
		}
	})

	t.Run("испорченный hash", func(t *testing.T) {
		data := signInitData(t, testToken, validFields(now))
		i := strings.Index(data, "hash=")
		forged := data[:i+5] + "00" + data[i+7:]
		if _, err := VerifyInitData(forged, testToken, now); !errors.Is(err, ErrBadSignature) {
			t.Errorf("ошибка = %v, ожидалось ErrBadSignature", err)
		}
	})

	t.Run("hash не шестнадцатеричный", func(t *testing.T) {
		data := signInitData(t, testToken, validFields(now))
		i := strings.Index(data, "hash=")
		forged := data[:i+5] + "zz" + data[i+7:]
		if _, err := VerifyInitData(forged, testToken, now); !errors.Is(err, ErrBadInitData) {
			t.Errorf("ошибка = %v, ожидалось ErrBadInitData", err)
		}
	})
}

func TestVerifyInitDataRejectsEmptyAndBroken(t *testing.T) {
	now := time.Now()

	for _, c := range []struct {
		name string
		data string
		want error
	}{
		{"пусто", "", ErrNoInitData},
		{"пробелы", "   ", ErrNoInitData},
		{"без hash", "auth_date=1&user=%7B%22id%22%3A1%7D", ErrBadInitData},
		{"мусор", "%%%", ErrBadInitData},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := VerifyInitData(c.data, testToken, now); !errors.Is(err, c.want) {
				t.Errorf("ошибка = %v, ожидалось %v", err, c.want)
			}
		})
	}
}

func TestVerifyInitDataChecksFreshness(t *testing.T) {
	now := time.Now()

	t.Run("протухшая подпись", func(t *testing.T) {
		old := now.Add(-InitDataTTL - time.Minute)
		data := signInitData(t, testToken, validFields(old))
		if _, err := VerifyInitData(data, testToken, now); !errors.Is(err, ErrStaleInitData) {
			t.Errorf("ошибка = %v, ожидалось ErrStaleInitData", err)
		}
	})

	t.Run("в пределах суток — годится", func(t *testing.T) {
		// Telegram выдаёт initData один раз за открытие и не обновляет:
		// короткий срок ломал бы оставленное открытым приложение.
		recent := now.Add(-InitDataTTL + time.Hour)
		data := signInitData(t, testToken, validFields(recent))
		if _, err := VerifyInitData(data, testToken, now); err != nil {
			t.Errorf("подпись возрастом меньше суток должна приниматься: %v", err)
		}
	})

	t.Run("минута расхождения часов прощается", func(t *testing.T) {
		ahead := now.Add(30 * time.Second)
		data := signInitData(t, testToken, validFields(ahead))
		if _, err := VerifyInitData(data, testToken, now); err != nil {
			t.Errorf("небольшой сдвиг часов не должен быть отказом: %v", err)
		}
	})

	t.Run("час из будущего — уже подозрительно", func(t *testing.T) {
		future := now.Add(time.Hour)
		data := signInitData(t, testToken, validFields(future))
		if _, err := VerifyInitData(data, testToken, now); !errors.Is(err, ErrStaleInitData) {
			t.Errorf("ошибка = %v, ожидалось ErrStaleInitData", err)
		}
	})
}

func TestVerifyInitDataNeedsUser(t *testing.T) {
	now := time.Now()

	t.Run("без пользователя", func(t *testing.T) {
		// Приложение открыли не из личного чата: записывать траты некому.
		fields := validFields(now)
		delete(fields, "user")
		data := signInitData(t, testToken, fields)

		if _, err := VerifyInitData(data, testToken, now); !errors.Is(err, ErrNoUser) {
			t.Errorf("ошибка = %v, ожидалось ErrNoUser", err)
		}
	})

	t.Run("пользователь не разбирается", func(t *testing.T) {
		fields := validFields(now)
		fields["user"] = "не json"
		data := signInitData(t, testToken, fields)

		if _, err := VerifyInitData(data, testToken, now); !errors.Is(err, ErrNoUser) {
			t.Errorf("ошибка = %v, ожидалось ErrNoUser", err)
		}
	})
}

func TestVerifyInitDataIgnoresThirdPartySignature(t *testing.T) {
	// Telegram добавляет поле signature для сторонней проверки. В строку
	// подписи бота оно не входит, и его наличие не должно всё ломать.
	now := time.Now()
	fields := validFields(now)
	data := signInitData(t, testToken, fields) + "&signature=" + url.QueryEscape("abc.def")

	if _, err := VerifyInitData(data, testToken, now); err != nil {
		t.Errorf("поле signature не должно мешать проверке: %v", err)
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		user TelegramUser
		want string
	}{
		{TelegramUser{FirstName: "Илья", LastName: "Петров"}, "Илья Петров"},
		{TelegramUser{FirstName: "Аня"}, "Аня"},
		{TelegramUser{Username: "ilya"}, "ilya"},
		{TelegramUser{}, "Без имени"},
	}
	for _, c := range cases {
		if got := c.user.DisplayName(); got != c.want {
			t.Errorf("DisplayName(%+v) = %q, ожидалось %q", c.user, got, c.want)
		}
	}
}

func TestCheckStringFormatIsPinnedByHand(t *testing.T) {
	// Всё остальное здесь подписывается тем же кодом, что и проверяется:
	// такой тест поймает поломку, но не поймает неверно понятый алгоритм.
	// Поэтому проверочная строка выписана руками, по документации Telegram:
	// поля по алфавиту, через перевод строки, значения раскодированные.
	const checkString = "auth_date=1700000000\n" +
		"chat_type=private\n" +
		`user={"id":777,"first_name":"Илья"}`

	secret := hmacSHA256([]byte("WebAppData"), testToken)
	hash := hex.EncodeToString(hmacSHA256(secret, checkString))

	q := url.Values{}
	q.Set("auth_date", "1700000000")
	q.Set("chat_type", "private")
	q.Set("user", `{"id":777,"first_name":"Илья"}`)
	q.Set("hash", hash)

	now := time.Unix(1700000000, 0).Add(time.Hour)
	user, err := VerifyInitData(q.Encode(), testToken, now)
	if err != nil {
		t.Fatalf("подпись по документации Telegram должна приниматься: %v", err)
	}
	if user.ID != 777 || user.FirstName != "Илья" {
		t.Errorf("пользователь = %+v, ожидался Илья (777)", user)
	}
}
