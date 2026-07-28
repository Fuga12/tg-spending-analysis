// Package auth — общие для бота и веба примитивы входа: одноразовые токены
// ссылок и токены сессий.
//
// Паролей и регистрации нет (plan.md §14). Доверенный канал — бот: он выдаёт
// ссылку, сайт обменивает её на сессию (webapp.md §1).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"time"
)

// Сроки жизни. Ссылка живёт минуты — этого достаточно, чтобы перейти по ней
// из чата; сессия месяц, чтобы не входить заново каждый день.
const (
	LoginTokenTTL = 5 * time.Minute
	SessionTTL    = 30 * 24 * time.Hour
)

// CookieName — имя cookie сессии.
const CookieName = "budget_session"

// tokenBytes — 32 байта из crypto/rand: перебирать нечего.
const tokenBytes = 32

// NewToken выдаёт токен для ссылки или сессии и его хэш для базы.
// Сам токен нигде не хранится: дамп базы не должен давать вход.
func NewToken() (token string, hash []byte, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("не смог сгенерировать токен: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, Hash(token), nil
}

// Hash считает хэш токена так же, как он лежит в базе.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// SameHash сравнивает хэши за постоянное время.
func SameHash(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// LoginURL собирает ссылку входа из публичного адреса сервиса.
func LoginURL(baseURL, token string) string {
	return baseURL + "/auth?token=" + token
}
