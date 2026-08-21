package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Invite зовёт человека в группу.
//
// Зовёт администратор, и только того, кто уже писал боту: приглашение по
// логину или телефону мы бы никуда не смогли доставить, а связать его с
// аккаунтом — только на честном слове.
func (g *GroupStore) Invite(ctx context.Context, byUserID, inviteeUserID int64) (Invite, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return Invite{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inviter, err := memberOfGroup(ctx, tx, g.groupID, byUserID)
	if err != nil {
		return Invite{}, err
	}
	if !inviter.IsAdmin() {
		return Invite{}, ErrNotAdmin
	}

	var inviteeName string
	err = tx.QueryRow(ctx, `select name from users where id = $1`, inviteeUserID).Scan(&inviteeName)
	if isNoRows(err) {
		return Invite{}, ErrUnknownUser
	}
	if err != nil {
		return Invite{}, err
	}

	var inGroup bool
	if err := tx.QueryRow(ctx, `
		select exists (select 1 from members where user_id = $1 and left_at is null)`,
		inviteeUserID).Scan(&inGroup); err != nil {
		return Invite{}, err
	}
	if inGroup {
		return Invite{}, ErrAlreadyMember
	}

	// Просроченное приглашение удаляется, а не помечается отклонённым: никто
	// его не отклонял, а хранить неотвеченное письмо годовой давности незачем.
	// Заодно оно перестаёт держать уникальный индекс открытых приглашений.
	if _, err := tx.Exec(ctx, `
		delete from invites
		where group_id = $1 and invitee_user_id = $2
		  and accepted_at is null and declined_at is null and expires_at <= now()`,
		g.groupID, inviteeUserID); err != nil {
		return Invite{}, err
	}

	if err := checkRoom(ctx, tx, g.groupID); err != nil {
		return Invite{}, err
	}

	inv := Invite{
		GroupID:       g.groupID,
		InviteeUserID: inviteeUserID,
		InvitedBy:     byUserID,
		InviterName:   inviter.Name,
	}
	err = tx.QueryRow(ctx, `
		insert into invites (group_id, invitee_user_id, invited_by, expires_at)
		values ($1, $2, $3, now() + $4::interval)
		returning id, created_at, expires_at`,
		g.groupID, inviteeUserID, byUserID, InviteTTL.String()).
		Scan(&inv.ID, &inv.CreatedAt, &inv.ExpiresAt)
	if err != nil {
		return Invite{}, err
	}
	if err := tx.QueryRow(ctx,
		`select name from groups where id = $1`, g.groupID).Scan(&inv.GroupName); err != nil {
		return Invite{}, err
	}
	return inv, tx.Commit(ctx)
}

// PendingInvites — открытые приглашения человека.
//
// Метод административный и по-другому быть не может: тот, кого зовут, ни в
// какой группе ещё не состоит, и сузить хранилище ему не до чего.
func (s *Store) PendingInvites(ctx context.Context, userID int64) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, `
		select i.id, i.group_id, gr.name, i.invitee_user_id, i.invited_by, u.name,
		       i.created_at, i.expires_at
		from invites i
		join groups gr on gr.id = i.group_id
		join users u on u.id = i.invited_by
		where i.invitee_user_id = $1
		  and i.accepted_at is null and i.declined_at is null
		  and i.expires_at > now()
		order by i.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.GroupID, &inv.GroupName, &inv.InviteeUserID,
			&inv.InvitedBy, &inv.InviterName, &inv.CreatedAt, &inv.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// AcceptInvite вводит приглашённого в группу.
//
// Принимает только тот, кого звали: id приглашения приходит из кнопки, а
// кнопку можно нажать чужую.
func (s *Store) AcceptInvite(ctx context.Context, inviteeUserID, inviteID int64) (Member, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Member{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		groupID   int64
		expiresAt time.Time
	)
	err = tx.QueryRow(ctx, `
		select group_id, expires_at from invites
		where id = $1 and invitee_user_id = $2
		  and accepted_at is null and declined_at is null
		for update`, inviteID, inviteeUserID).Scan(&groupID, &expiresAt)
	if isNoRows(err) {
		return Member{}, ErrNoInvite
	}
	if err != nil {
		return Member{}, err
	}
	if !expiresAt.After(time.Now()) {
		return Member{}, ErrInviteExpired
	}

	var inGroup bool
	if err := tx.QueryRow(ctx, `
		select exists (select 1 from members where user_id = $1 and left_at is null)`,
		inviteeUserID).Scan(&inGroup); err != nil {
		return Member{}, err
	}
	if inGroup {
		return Member{}, ErrAlreadyMember
	}
	if err := checkRoom(ctx, tx, groupID); err != nil {
		return Member{}, err
	}

	if _, err := tx.Exec(ctx,
		`update invites set accepted_at = now() where id = $1`, inviteID); err != nil {
		return Member{}, err
	}

	m, err := joinGroup(ctx, tx, groupID, inviteeUserID, RoleMember)
	if err != nil {
		return Member{}, err
	}
	return m, tx.Commit(ctx)
}

// DeclineInvite отказывается от приглашения.
func (s *Store) DeclineInvite(ctx context.Context, inviteeUserID, inviteID int64) error {
	tag, err := s.pool.Exec(ctx, `
		update invites set declined_at = now()
		where id = $1 and invitee_user_id = $2
		  and accepted_at is null and declined_at is null`, inviteID, inviteeUserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoInvite
	}
	return nil
}

// checkRoom проверяет, что в группе есть место. Открытые приглашения тоже
// занимают место: иначе десять приглашений разом дают одиннадцатого участника,
// и виноват в этом окажется тот, кто просто нажал «принять».
func checkRoom(ctx context.Context, tx pgx.Tx, groupID int64) error {
	var taken int
	err := tx.QueryRow(ctx, `
		select (select count(*) from members
		        where group_id = $1 and left_at is null)
		     + (select count(*) from invites
		        where group_id = $1 and accepted_at is null and declined_at is null
		          and expires_at > now())`, groupID).Scan(&taken)
	if err != nil {
		return err
	}
	if taken >= MaxGroupSize {
		return ErrGroupFull
	}
	return nil
}

// joinGroup вводит человека в группу.
//
// Вернувшемуся возвращается его прежняя строка, а не заводится новая: на
// старую ссылаются его траты, и вторая строка развела бы одного человека на
// две в отчёте — «Аня» и ещё раз «Аня».
func joinGroup(ctx context.Context, tx pgx.Tx, groupID, userID int64, role string) (Member, error) {
	m := Member{GroupID: groupID, UserID: userID, Role: role}

	err := tx.QueryRow(ctx, `
		update members set left_at = null, role = $3
		where group_id = $1 and user_id = $2 and left_at is not null
		returning id, joined_at`, groupID, userID, role).Scan(&m.ID, &m.JoinedAt)
	if isNoRows(err) {
		err = tx.QueryRow(ctx, `
			insert into members (group_id, user_id, role) values ($1, $2, $3)
			returning id, joined_at`, groupID, userID, role).Scan(&m.ID, &m.JoinedAt)
	}
	if err != nil {
		return Member{}, err
	}
	if err := tx.QueryRow(ctx,
		`select name from users where id = $1`, userID).Scan(&m.Name); err != nil {
		return Member{}, err
	}
	return m, nil
}

// memberOfGroup читает участника внутри транзакции.
func memberOfGroup(ctx context.Context, tx pgx.Tx, groupID, userID int64) (Member, error) {
	var m Member
	err := tx.QueryRow(ctx, `
		select m.id, m.group_id, m.user_id, u.name, m.role, m.joined_at
		from members m
		join users u on u.id = m.user_id
		where m.group_id = $1 and m.user_id = $2 and m.left_at is null`, groupID, userID).
		Scan(&m.ID, &m.GroupID, &m.UserID, &m.Name, &m.Role, &m.JoinedAt)
	if isNoRows(err) {
		return Member{}, ErrNotMember
	}
	return m, err
}
