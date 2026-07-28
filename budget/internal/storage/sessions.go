package storage

import (
	"context"
	"errors"
	"time"
)

// ErrNoSession — сессии нет, она протухла или токен уже использован.
var ErrNoSession = errors.New("сессия не найдена")

// CreateLoginToken кладёт хэш одноразовой ссылки входа.
func (s *Store) CreateLoginToken(ctx context.Context, hash []byte, userID int64, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		insert into login_tokens (token_hash, user_id, expires_at)
		values ($1, $2, now() + $3::interval)`,
		hash, userID, ttl.String())
	return err
}

// ConsumeLoginToken гасит ссылку и отдаёт владельца. Гашение и проверка —
// один запрос: иначе по ссылке можно войти дважды, успев между проверкой
// и отметкой.
func (s *Store) ConsumeLoginToken(ctx context.Context, hash []byte) (int64, error) {
	var userID int64
	err := s.pool.QueryRow(ctx, `
		update login_tokens set used_at = now()
		where token_hash = $1 and used_at is null and expires_at > now()
		returning user_id`, hash).Scan(&userID)
	if err != nil {
		if isNoRows(err) {
			return 0, ErrNoSession
		}
		return 0, err
	}
	return userID, nil
}

// CreateSession заводит сессию по хэшу токена.
func (s *Store) CreateSession(ctx context.Context, hash []byte, userID int64, ttl time.Duration, userAgent string) error {
	_, err := s.pool.Exec(ctx, `
		insert into web_sessions (token_hash, user_id, expires_at, user_agent)
		values ($1, $2, now() + $3::interval, $4)`,
		hash, userID, ttl.String(), trimTo(userAgent, 300))
	return err
}

// SessionUser отдаёт владельца живой сессии.
func (s *Store) SessionUser(ctx context.Context, hash []byte) (int64, error) {
	var userID int64
	err := s.pool.QueryRow(ctx, `
		select user_id from web_sessions
		where token_hash = $1 and expires_at > now()`, hash).Scan(&userID)
	if err != nil {
		if isNoRows(err) {
			return 0, ErrNoSession
		}
		return 0, err
	}
	return userID, nil
}

// TouchSession отмечает, что сессией пользовались. Реже раза в час в базу не
// пишем: статистика того не стоит.
func (s *Store) TouchSession(ctx context.Context, hash []byte) error {
	_, err := s.pool.Exec(ctx, `
		update web_sessions set last_seen_at = now()
		where token_hash = $1 and last_seen_at < now() - interval '1 hour'`, hash)
	return err
}

// DeleteSession — выход с этого устройства.
func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.pool.Exec(ctx, `delete from web_sessions where token_hash = $1`, hash)
	return err
}

// DeleteUserSessions — выход отовсюду.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `delete from web_sessions where user_id = $1`, userID)
	return err
}

// CleanupAuth убирает протухшее. Вызывается при входе — отдельного крона на
// пару строк в неделю заводить незачем.
func (s *Store) CleanupAuth(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		delete from login_tokens where expires_at < now() - interval '1 day'`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `delete from web_sessions where expires_at < now()`)
	return err
}

// trimTo обрезает строку до n символов, не разрывая руны.
func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
