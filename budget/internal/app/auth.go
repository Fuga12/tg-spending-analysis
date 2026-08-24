// Package app — Mini App: HTTP-сервер, API поверх хранилища и отдача фронта.
//
// Единственная дверь внутрь — подпись Telegram. Браузерной ветки нет: ни
// паролей, ни сессий, ни ссылок входа. Значит, ошибка в проверке подписи —
// это либо «внутрь не попасть ничем», либо «внутрь попадёт кто угодно», и
// третьего не дано. Поэтому проверка написана и покрыта тестами первой.
package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// InitDataTTL — сколько подпись считается свежей.
//
// Сутки, а не минуты: Telegram выдаёт initData один раз при открытии
// приложения и не обновляет его, пока окно живо. Короткий срок означал бы,
// что оставленное открытым приложение перестаёт работать посреди дня.
const InitDataTTL = 24 * time.Hour

// Ошибки проверки. Наружу они не показываются подробностями — клиенту
// достаточно 401, — но в логе различать их нужно: «подпись не сошлась» и
// «протухло» это разные поводы для беспокойства.
var (
	ErrNoInitData    = errors.New("подписи нет")
	ErrBadInitData   = errors.New("подпись не разобрать")
	ErrBadSignature  = errors.New("подпись не сошлась")
	ErrStaleInitData = errors.New("подпись просрочена")
	ErrNoUser        = errors.New("в подписи нет пользователя")
)

// TelegramUser — то, что Telegram кладёт в initData про открывшего.
type TelegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// DisplayName — имя, под которым человек заводится при первом заходе.
// Дальше он меняет его сам, и EnsureUser его не перезатирает.
func (u TelegramUser) DisplayName() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	if name == "" {
		name = "Без имени"
	}
	return name
}

// VerifyInitData проверяет подпись Telegram и возвращает открывшего.
//
// Алгоритм задан Telegram и целиком: пары сортируются по ключу, склеиваются
// через перевод строки, ключ HMAC выводится из токена бота с солью
// «WebAppData». Отступать тут не от чего — любое отступление означает, что
// подпись проверяется не та.
func VerifyInitData(initData, botToken string, now time.Time) (TelegramUser, error) {
	if strings.TrimSpace(initData) == "" {
		return TelegramUser{}, ErrNoInitData
	}

	// Разбираем сами, а не url.ParseQuery: нам нужно исходное значение
	// каждой пары ровно в том виде, в каком её подписали, а ParseQuery
	// возвращает уже раскодированные и в произвольном порядке.
	values, err := url.ParseQuery(initData)
	if err != nil {
		return TelegramUser{}, ErrBadInitData
	}

	hash := values.Get("hash")
	if hash == "" {
		return TelegramUser{}, ErrBadInitData
	}

	pairs := make([]string, 0, len(values))
	for key, list := range values {
		// signature — подпись третьих лиц (Telegram Ads и подобное), в
		// проверочную строку она не входит наравне с hash.
		if key == "hash" || key == "signature" {
			continue
		}
		pairs = append(pairs, key+"="+list[0])
	}
	sort.Strings(pairs)

	secret := hmacSHA256([]byte("WebAppData"), botToken)
	want := hmacSHA256(secret, strings.Join(pairs, "\n"))

	got, err := hex.DecodeString(hash)
	if err != nil {
		return TelegramUser{}, ErrBadInitData
	}
	// Константное сравнение: обычное даёт по байту за попытку, а подобрать
	// подпись байт за байтом — это тысячи запросов, а не миллиарды.
	if !hmac.Equal(got, want) {
		return TelegramUser{}, ErrBadSignature
	}

	// Свежесть проверяется только после подписи: до неё auth_date — это
	// просто число, которое прислал кто угодно.
	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return TelegramUser{}, ErrBadInitData
	}
	if age := now.Sub(time.Unix(authDate, 0)); age > InitDataTTL || age < -time.Minute {
		// Отрицательный возраст — это часы, ушедшие вперёд у нас или у
		// Telegram. Минуту прощаем, больше — уже подозрительно.
		return TelegramUser{}, ErrStaleInitData
	}

	raw := values.Get("user")
	if raw == "" {
		// Приложение открыли не из личного чата — например, из инлайн-режима.
		// Записывать траты в этом случае некому.
		return TelegramUser{}, ErrNoUser
	}
	var user TelegramUser
	if err := json.Unmarshal([]byte(raw), &user); err != nil || user.ID == 0 {
		return TelegramUser{}, ErrNoUser
	}
	return user, nil
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
