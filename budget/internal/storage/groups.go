package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Правила группы. Все проверяются внутри транзакции вместе с самим
// изменением: между «можно ли» и «делаем» иначе успевает вклиниться второе
// нажатие, и группа остаётся без администратора или с одиннадцатым
// участником.
var (
	ErrNotAdmin      = errors.New("нужны права администратора")
	ErrNotMember     = errors.New("не участник этой группы")
	ErrGroupFull     = errors.New("в группе больше нет мест")
	ErrAlreadyMember = errors.New("человек уже состоит в группе")
	ErrUnknownUser   = errors.New("человек ещё не писал боту")
	ErrLastAdmin     = errors.New("группа останется без администратора")
	ErrNoInvite      = errors.New("приглашения нет")
	ErrInviteExpired = errors.New("приглашение просрочено")
	ErrBadRole       = errors.New("такой роли не бывает")
)

// InviteTTL — сколько живёт приглашение. Неделя: приглашение, о котором
// забыли, не должно висеть вечно и занимать место в группе.
const InviteTTL = 7 * 24 * time.Hour

// Invite — приглашение в группу. Приглашают только того, кто уже стартовал
// бота: иначе некуда прислать уведомление и не с чем связать аккаунт.
type Invite struct {
	ID            int64
	GroupID       int64
	GroupName     string
	InviteeUserID int64
	InvitedBy     int64
	InviterName   string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

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

	// Через тот же joinGroup, что и все остальные: иначе создатель приезжает
	// без имени, и это всплывает через полгода в чужом уведомлении.
	m, err := joinGroup(ctx, tx, g.ID, ownerUserID, RoleAdmin)
	if err != nil {
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

// AddMember вводит человека в группу напрямую, без приглашения.
//
// Обычный путь — Invite и AcceptInvite: человека нельзя записать в группу
// без его согласия. Этот метод нужен там, где согласие уже есть по построению:
// первый участник при создании группы и подготовка данных в тестах.
//
// Правило «один человек — одна группа» держит частичный уникальный индекс:
// вызов для того, кто уже где-то состоит, вернёт ошибку, а не молча перевесит
// его в другую группу.
func (g *GroupStore) AddMember(ctx context.Context, userID int64, role string) (Member, error) {
	var m Member
	err := g.inTx(ctx, func(tx pgx.Tx) error {
		if err := checkRoom(ctx, tx, g.groupID); err != nil {
			return err
		}
		var err error
		m, err = joinGroup(ctx, tx, g.groupID, userID, role)
		return err
	})
	if err != nil {
		return Member{}, err
	}
	return m, nil
}

// Leave — участник уходит сам.
func (g *GroupStore) Leave(ctx context.Context, userID int64) error {
	return g.inTx(ctx, func(tx pgx.Tx) error {
		m, err := memberOfGroup(ctx, tx, g.groupID, userID)
		if err != nil {
			return err
		}
		return leaveGroup(ctx, tx, g.groupID, m)
	})
}

// RemoveMember — администратор исключает другого участника.
//
// Себя через этот метод исключить нельзя: «выйти» и «выгнать» — разные
// действия, и подтверждение у них разное. Для себя есть Leave.
func (g *GroupStore) RemoveMember(ctx context.Context, byUserID, memberID int64) error {
	return g.inTx(ctx, func(tx pgx.Tx) error {
		admin, err := memberOfGroup(ctx, tx, g.groupID, byUserID)
		if err != nil {
			return err
		}
		if !admin.IsAdmin() {
			return ErrNotAdmin
		}
		if admin.ID == memberID {
			return ErrNotAdmin
		}

		target, err := memberByID(ctx, tx, g.groupID, memberID)
		if err != nil {
			return err
		}
		return leaveGroup(ctx, tx, g.groupID, target)
	})
}

// SetRole повышает участника до администратора или разжалует обратно.
func (g *GroupStore) SetRole(ctx context.Context, byUserID, memberID int64, role string) error {
	if role != RoleAdmin && role != RoleMember {
		return ErrBadRole
	}
	return g.inTx(ctx, func(tx pgx.Tx) error {
		admin, err := memberOfGroup(ctx, tx, g.groupID, byUserID)
		if err != nil {
			return err
		}
		if !admin.IsAdmin() {
			return ErrNotAdmin
		}

		target, err := memberByID(ctx, tx, g.groupID, memberID)
		if err != nil {
			return err
		}
		if target.Role == role {
			return nil
		}
		// Разжаловать последнего администратора — то же самое, что оставить
		// группу без хозяина: некому будет ни звать, ни исключать.
		if role == RoleMember {
			if err := requireAnotherAdmin(ctx, tx, g.groupID, target.ID); err != nil {
				return err
			}
		}

		_, err = tx.Exec(ctx,
			`update members set role = $3 where id = $2 and group_id = $1`,
			g.groupID, memberID, role)
		return err
	})
}

// leaveGroup помечает участника ушедшим. Строка остаётся: на неё ссылаются
// его траты, и без неё история группы теряет плательщика.
func leaveGroup(ctx context.Context, tx pgx.Tx, groupID int64, m Member) error {
	if m.IsAdmin() {
		if err := requireAnotherAdmin(ctx, tx, groupID, m.ID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx,
		`update members set left_at = now() where id = $1 and left_at is null`, m.ID)
	return err
}

// requireAnotherAdmin убеждается, что кроме этого участника в группе есть
// ещё хотя бы один администратор.
func requireAnotherAdmin(ctx context.Context, tx pgx.Tx, groupID, exceptMemberID int64) error {
	var others int
	err := tx.QueryRow(ctx, `
		select count(*) from members
		where group_id = $1 and id <> $2 and role = 'admin' and left_at is null`,
		groupID, exceptMemberID).Scan(&others)
	if err != nil {
		return err
	}
	if others == 0 {
		return ErrLastAdmin
	}
	return nil
}

// memberByID читает действующего участника группы внутри транзакции.
func memberByID(ctx context.Context, tx pgx.Tx, groupID, memberID int64) (Member, error) {
	var m Member
	err := tx.QueryRow(ctx, `
		select m.id, m.group_id, m.user_id, u.name, m.role, m.joined_at
		from members m
		join users u on u.id = m.user_id
		where m.group_id = $1 and m.id = $2 and m.left_at is null`, groupID, memberID).
		Scan(&m.ID, &m.GroupID, &m.UserID, &m.Name, &m.Role, &m.JoinedAt)
	if isNoRows(err) {
		return Member{}, ErrNotMember
	}
	return m, err
}

// inTx выполняет изменение одной транзакцией: проверка правила и само
// изменение не должны разъезжаться во времени.
func (g *GroupStore) inTx(ctx context.Context, do func(pgx.Tx) error) error {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := do(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
