package storage

import (
	"context"
	"time"
)

// User — участник бюджета из whitelist.
type User struct {
	ID   int64
	Name string
	// AvatarAt — когда поставили своё фото профиля; nil, если своего нет и
	// аватарка берётся у Telegram. Он же версия картинки для URL.
	AvatarAt *time.Time
}

// UpsertUser заводит пользователя или обновляет имя, если оно изменилось.
func (s *Store) UpsertUser(ctx context.Context, id int64, name string) error {
	_, err := s.pool.Exec(ctx, `
		insert into users (id, name) values ($1, $2)
		on conflict (id) do nothing`, id, name)
	return err
}

// Users возвращает всех известных боту пользователей. Нужен отчёту: чтобы
// понять, кто такой partner, надо знать второго (§10).
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `select id, name, avatar_set_at from users order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.AvatarAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// EnsureUser заводит пользователя, если его ещё нет, и не трогает имя
// существующего: перезаписывать чужое имя служебным нельзя.
func (s *Store) EnsureUser(ctx context.Context, id int64, name string) error {
	_, err := s.pool.Exec(ctx, `
		insert into users (id, name) values ($1, $2)
		on conflict (id) do nothing`, id, name)
	return err
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
