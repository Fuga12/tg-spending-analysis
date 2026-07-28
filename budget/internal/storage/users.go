package storage

import "context"

// User — участник бюджета из whitelist.
type User struct {
	ID   int64
	Name string
}

// UpsertUser заводит пользователя или обновляет имя, если оно изменилось.
func (s *Store) UpsertUser(ctx context.Context, id int64, name string) error {
	_, err := s.pool.Exec(ctx, `
		insert into users (id, name) values ($1, $2)
		on conflict (id) do update set name = excluded.name`, id, name)
	return err
}
