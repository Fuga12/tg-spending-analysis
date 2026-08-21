package storage

import (
	"context"
	"time"
)

// Роли участника. Администратор зовёт и исключает, остальное умеют оба.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// MaxGroupSize — потолок участников. Десять — предел, на котором список
// получателей ещё читается человеком, а не превращается в таблицу.
const MaxGroupSize = 10

// Group — строка таблицы groups.
type Group struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

// Member — участник группы. Name приезжает из users: имя у человека одно
// на все группы, в которых он когда-либо был.
type Member struct {
	ID       int64
	GroupID  int64
	UserID   int64
	Name     string
	Role     string
	JoinedAt time.Time
	LeftAt   *time.Time
}

// IsAdmin — может ли участник звать и исключать.
func (m Member) IsAdmin() bool { return m.Role == RoleAdmin }

// CreateGroup заводит группу вместе с первым участником-администратором и
// раскатывает ей категории из шаблона.
//
// Всё одной транзакцией: группа без администратора никому не принадлежит, а
// группа без категорий не может принять ни одной траты. Оставить систему
// в таком состоянии из-за оборванного соединения нельзя.
func (s *Store) CreateGroup(ctx context.Context, name string, ownerUserID int64) (Group, Member, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Group{}, Member{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var g Group
	g.Name = trimTo(name, MaxGroupNameLen)
	if err := tx.QueryRow(ctx,
		`insert into groups (name) values ($1) returning id, created_at`,
		g.Name).Scan(&g.ID, &g.CreatedAt); err != nil {
		return Group{}, Member{}, err
	}

	m := Member{GroupID: g.ID, UserID: ownerUserID, Role: RoleAdmin}
	if err := tx.QueryRow(ctx, `
		insert into members (group_id, user_id, role) values ($1, $2, $3)
		returning id, joined_at`,
		g.ID, ownerUserID, RoleAdmin).Scan(&m.ID, &m.JoinedAt); err != nil {
		return Group{}, Member{}, err
	}

	if _, err := tx.Exec(ctx, `
		insert into categories (group_id, name, hint, template_key, sort_order)
		select $1, name, hint, template_key, sort_order from category_template`,
		g.ID); err != nil {
		return Group{}, Member{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Group{}, Member{}, err
	}
	return g, m, nil
}

// MaxGroupNameLen — потолок длины названия группы.
const MaxGroupNameLen = 64

// MemberOf — в какой группе состоит человек. Второе значение false означает,
// что он не состоит ни в одной: бот в этом случае не может записать трату,
// и это нормальное состояние только что пришедшего.
func (s *Store) MemberOf(ctx context.Context, userID int64) (Member, bool, error) {
	var m Member
	err := s.pool.QueryRow(ctx, `
		select m.id, m.group_id, m.user_id, u.name, m.role, m.joined_at
		from members m
		join users u on u.id = m.user_id
		where m.user_id = $1 and m.left_at is null`, userID).
		Scan(&m.ID, &m.GroupID, &m.UserID, &m.Name, &m.Role, &m.JoinedAt)
	if isNoRows(err) {
		return Member{}, false, nil
	}
	if err != nil {
		return Member{}, false, err
	}
	return m, true, nil
}

// AddMember вводит человека в группу.
//
// Правило «один человек — одна группа» держит частичный уникальный индекс:
// повторный вызов для того, кто уже где-то состоит, вернёт ошибку, а не
// молча перевесит его в другую группу.
func (g *GroupStore) AddMember(ctx context.Context, userID int64, role string) (Member, error) {
	m := Member{GroupID: g.groupID, UserID: userID, Role: role}
	err := g.pool.QueryRow(ctx, `
		insert into members (group_id, user_id, role) values ($1, $2, $3)
		returning id, joined_at`, g.groupID, userID, role).Scan(&m.ID, &m.JoinedAt)
	if err != nil {
		return Member{}, err
	}
	if err := g.pool.QueryRow(ctx,
		`select name from users where id = $1`, userID).Scan(&m.Name); err != nil {
		return Member{}, err
	}
	return m, nil
}

// Members — действующие участники группы, в порядке вступления.
func (g *GroupStore) Members(ctx context.Context) ([]Member, error) {
	return g.members(ctx, `and m.left_at is null`)
}

// AllMembers — участники вместе с ушедшими. Нужен отчётам: траты человека,
// покинувшего группу, из истории никуда не делись, и подписать их надо.
func (g *GroupStore) AllMembers(ctx context.Context) ([]Member, error) {
	return g.members(ctx, ``)
}

func (g *GroupStore) members(ctx context.Context, extra string) ([]Member, error) {
	rows, err := g.pool.Query(ctx, `
		select m.id, m.group_id, m.user_id, u.name, m.role, m.joined_at, m.left_at
		from members m
		join users u on u.id = m.user_id
		where m.group_id = $1 `+extra+`
		order by m.joined_at, m.id`, g.groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.GroupID, &m.UserID, &m.Name, &m.Role,
			&m.JoinedAt, &m.LeftAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Group — сама группа.
func (g *GroupStore) Group(ctx context.Context) (Group, error) {
	var out Group
	err := g.pool.QueryRow(ctx,
		`select id, name, created_at from groups where id = $1`, g.groupID).
		Scan(&out.ID, &out.Name, &out.CreatedAt)
	return out, err
}
