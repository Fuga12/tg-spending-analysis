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

// Users возвращает всех известных боту пользователей. Нужен отчёту: чтобы
// понять, кто такой partner, надо знать второго (§10).
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `select id, name from users order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
