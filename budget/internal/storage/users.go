package storage

import (
	"context"
	"time"
)

// MaxNameLen — потолок длины имени.
const MaxNameLen = 32

// EnsureUser заводит пользователя при первом обращении.
//
// Имя приходит из профиля Telegram — в открытом боте взять его больше
// неоткуда. Но только при заведении: `do nothing` защищает имя, которое
// человек потом поставит себе сам в приложении, от затирания на каждом
// сообщении. Telegram зовёт «Ульяночка», в бюджете она Уля.
func (s *Store) EnsureUser(ctx context.Context, id int64, name string) error {
	_, err := s.pool.Exec(ctx, `
		insert into users (id, name) values ($1, $2)
		on conflict (id) do nothing`, id, trimTo(name, MaxNameLen))
	return err
}

// SetName ставит имя, выбранное самим человеком в приложении.
func (s *Store) SetName(ctx context.Context, id int64, name string) error {
	_, err := s.pool.Exec(ctx, `update users set name = $2 where id = $1`,
		id, trimTo(name, MaxNameLen))
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

// Avatar — своё фото профиля. nil означает, что его не ставили: тогда
// аватарка берётся у Telegram.
func (s *Store) Avatar(ctx context.Context, id int64) ([]byte, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `select avatar from users where id = $1`, id).Scan(&data)
	if isNoRows(err) {
		return nil, nil
	}
	return data, err
}

// SetAvatar ставит своё фото и возвращает его версию.
func (s *Store) SetAvatar(ctx context.Context, id int64, data []byte) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `
		update users set avatar = $2, avatar_set_at = now()
		where id = $1
		returning avatar_set_at`, id, data).Scan(&at)
	return at, err
}

// ClearAvatar убирает своё фото — аватарка возвращается к телеграмной.
func (s *Store) ClearAvatar(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		update users set avatar = null, avatar_set_at = null where id = $1`, id)
	return err
}

// User — человек, каким его знает приложение.
type User struct {
	ID   int64
	Name string
	// AvatarAt — когда поставили своё фото профиля; nil, если своего нет и
	// аватарка берётся у Telegram. Он же версия картинки для URL: без него
	// новое фото полсуток не видно из-за кэша браузера.
	AvatarAt *time.Time
}

// User читает одного человека.
func (s *Store) User(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`select id, name, avatar_set_at from users where id = $1`, id).
		Scan(&u.ID, &u.Name, &u.AvatarAt)
	if isNoRows(err) {
		return User{}, ErrNoRows
	}
	return u, err
}

// AvatarVersions отдаёт версии фото сразу для нескольких человек.
//
// Одним запросом, а не по одному на участника: версия уезжает в адрес
// картинки, и на группе из десяти это десять лишних round-trip ради
// десяти временных меток.
func (s *Store) AvatarVersions(ctx context.Context, ids []int64) (map[int64]*time.Time, error) {
	out := make(map[int64]*time.Time, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`select id, avatar_set_at from users where id = any($1) and avatar_set_at is not null`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id int64
			at time.Time
		)
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		when := at
		out[id] = &when
	}
	return out, rows.Err()
}
