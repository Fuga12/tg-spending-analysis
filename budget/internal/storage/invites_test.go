package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// newcomer заводит человека, который боту уже писал, но ни в какой группе
// не состоит, — единственный, кого можно позвать.
func newcomer(t *testing.T, s *Store, id int64, name string) int64 {
	t.Helper()
	if err := s.EnsureUser(context.Background(), id, name); err != nil {
		t.Fatalf("пользователь %d: %v", id, err)
	}
	return id
}

func TestInviteAndAccept(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)
	guest := newcomer(t, s, 50, "Гость")

	inv, err := g.Invite(ctx, members[0].UserID, guest)
	if err != nil {
		t.Fatalf("приглашение: %v", err)
	}
	// Уведомлению нужны имена, а не идентификаторы: «Илья зовёт тебя в
	// „Дом“» человек понимает, «пользователь 1 в группу 7» — нет.
	if inv.InviterName != members[0].Name || inv.GroupName == "" {
		t.Errorf("приглашение = %+v, ожидались имена приглашающего и группы", inv)
	}
	if !inv.ExpiresAt.After(time.Now()) {
		t.Errorf("приглашение просрочено сразу: %s", inv.ExpiresAt)
	}

	pending, err := s.PendingInvites(ctx, guest)
	if err != nil {
		t.Fatalf("открытые приглашения: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != inv.ID {
		t.Fatalf("открытых приглашений %+v, ожидалось одно", pending)
	}

	m, err := s.AcceptInvite(ctx, guest, inv.ID)
	if err != nil {
		t.Fatalf("принятие: %v", err)
	}
	if m.GroupID != g.GroupID() || m.Role != RoleMember {
		t.Errorf("участник = %+v, ожидался обычный участник этой группы", m)
	}

	// Принятое приглашение больше не открыто, и второй раз не срабатывает.
	if pending, _ = s.PendingInvites(ctx, guest); len(pending) != 0 {
		t.Errorf("после принятия осталось %d открытых приглашений", len(pending))
	}
	if _, err := s.AcceptInvite(ctx, guest, inv.ID); !errors.Is(err, ErrNoInvite) {
		t.Errorf("повторное принятие = %v, ожидалось ErrNoInvite", err)
	}
}

func TestOnlyAdminInvites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)
	guest := newcomer(t, s, 50, "Гость")

	if _, err := g.Invite(ctx, members[1].UserID, guest); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("приглашение от обычного участника = %v, ожидалось ErrNotAdmin", err)
	}
	// А посторонний не может пригласить и подавно.
	if _, err := g.Invite(ctx, 999, guest); !errors.Is(err, ErrNotMember) {
		t.Errorf("приглашение от чужого = %v, ожидалось ErrNotMember", err)
	}
}

func TestInviteOnlyThoseWhoStartedTheBot(t *testing.T) {
	// Приглашение по логину или телефону некуда доставить, и связать его
	// с аккаунтом можно только на честном слове.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	if _, err := g.Invite(ctx, members[0].UserID, 12345); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("приглашение незнакомца = %v, ожидалось ErrUnknownUser", err)
	}
}

func TestInviteRejectsThoseAlreadyInAGroup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	other := newcomer(t, s, 99, "Чужой")
	if _, _, err := s.CreateGroup(ctx, "Другая", other); err != nil {
		t.Fatalf("вторая группа: %v", err)
	}

	if _, err := g.Invite(ctx, members[0].UserID, other); !errors.Is(err, ErrAlreadyMember) {
		t.Errorf("приглашение занятого = %v, ожидалось ErrAlreadyMember", err)
	}
}

func TestAcceptRejectedWhenAlreadyJoinedElsewhere(t *testing.T) {
	// Приглашение выдано, пока человек был свободен, а принял он его уже
	// после вступления в другую группу. Между «позвали» и «принял» проходят
	// дни, и проверять надо в момент принятия.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)
	guest := newcomer(t, s, 50, "Гость")

	inv, err := g.Invite(ctx, members[0].UserID, guest)
	if err != nil {
		t.Fatalf("приглашение: %v", err)
	}
	if _, _, err := s.CreateGroup(ctx, "Своя", guest); err != nil {
		t.Fatalf("своя группа: %v", err)
	}

	if _, err := s.AcceptInvite(ctx, guest, inv.ID); !errors.Is(err, ErrAlreadyMember) {
		t.Errorf("принятие = %v, ожидалось ErrAlreadyMember", err)
	}
}

func TestOnlyInviteeAnswers(t *testing.T) {
	// Идентификатор приглашения лежит в кнопке, а кнопку можно переслать.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)
	guest := newcomer(t, s, 50, "Гость")
	stranger := newcomer(t, s, 51, "Посторонний")

	inv, err := g.Invite(ctx, members[0].UserID, guest)
	if err != nil {
		t.Fatalf("приглашение: %v", err)
	}

	if _, err := s.AcceptInvite(ctx, stranger, inv.ID); !errors.Is(err, ErrNoInvite) {
		t.Errorf("чужое принятие = %v, ожидалось ErrNoInvite", err)
	}
	if err := s.DeclineInvite(ctx, stranger, inv.ID); !errors.Is(err, ErrNoInvite) {
		t.Errorf("чужой отказ = %v, ожидалось ErrNoInvite", err)
	}
	// Приглашение при этом цело.
	if pending, _ := s.PendingInvites(ctx, guest); len(pending) != 1 {
		t.Errorf("открытых приглашений %+v, ожидалось одно", pending)
	}
}

func TestDeclineClosesInvite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)
	guest := newcomer(t, s, 50, "Гость")

	inv, err := g.Invite(ctx, members[0].UserID, guest)
	if err != nil {
		t.Fatalf("приглашение: %v", err)
	}
	if err := s.DeclineInvite(ctx, guest, inv.ID); err != nil {
		t.Fatalf("отказ: %v", err)
	}
	if pending, _ := s.PendingInvites(ctx, guest); len(pending) != 0 {
		t.Errorf("после отказа осталось %d открытых", len(pending))
	}
	if _, err := s.AcceptInvite(ctx, guest, inv.ID); !errors.Is(err, ErrNoInvite) {
		t.Errorf("принятие после отказа = %v, ожидалось ErrNoInvite", err)
	}

	// Отказавшегося можно позвать снова: отказ — не приговор.
	if _, err := g.Invite(ctx, members[0].UserID, guest); err != nil {
		t.Errorf("повторное приглашение после отказа: %v", err)
	}
}

func TestExpiredInviteCannotBeAcceptedButCanBeReissued(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)
	guest := newcomer(t, s, 50, "Гость")

	inv, err := g.Invite(ctx, members[0].UserID, guest)
	if err != nil {
		t.Fatalf("приглашение: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`update invites set expires_at = now() - interval '1 day' where id = $1`, inv.ID); err != nil {
		t.Fatalf("состаривание: %v", err)
	}

	if _, err := s.AcceptInvite(ctx, guest, inv.ID); !errors.Is(err, ErrInviteExpired) {
		t.Errorf("принятие просроченного = %v, ожидалось ErrInviteExpired", err)
	}
	if pending, _ := s.PendingInvites(ctx, guest); len(pending) != 0 {
		t.Errorf("просроченное приглашение не должно висеть открытым: %+v", pending)
	}

	// Просроченное не должно мешать позвать заново — иначе человека нельзя
	// пригласить уже никогда.
	fresh, err := g.Invite(ctx, members[0].UserID, guest)
	if err != nil {
		t.Fatalf("повторное приглашение: %v", err)
	}
	if _, err := s.AcceptInvite(ctx, guest, fresh.ID); err != nil {
		t.Errorf("принятие свежего: %v", err)
	}
}

func TestGroupSizeCeiling(t *testing.T) {
	// Десять — предел, на котором список получателей ещё читается человеком.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, MaxGroupSize)

	guest := newcomer(t, s, 60, "Лишний")
	if _, err := g.Invite(ctx, members[0].UserID, guest); !errors.Is(err, ErrGroupFull) {
		t.Errorf("приглашение в полную группу = %v, ожидалось ErrGroupFull", err)
	}
	if _, err := g.AddMember(ctx, guest, RoleMember); !errors.Is(err, ErrGroupFull) {
		t.Errorf("добавление в полную группу = %v, ожидалось ErrGroupFull", err)
	}
}

func TestOpenInvitesTakeUpRoom(t *testing.T) {
	// Иначе десять приглашений разом дают одиннадцатого участника, и виноват
	// окажется тот, кто просто нажал «принять».
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, MaxGroupSize-1)

	first := newcomer(t, s, 60, "Первый")
	second := newcomer(t, s, 61, "Второй")

	if _, err := g.Invite(ctx, members[0].UserID, first); err != nil {
		t.Fatalf("первое приглашение: %v", err)
	}
	if _, err := g.Invite(ctx, members[0].UserID, second); !errors.Is(err, ErrGroupFull) {
		t.Errorf("второе приглашение = %v, ожидалось ErrGroupFull", err)
	}

	// Отказ освобождает место.
	pending, _ := s.PendingInvites(ctx, first)
	if err := s.DeclineInvite(ctx, first, pending[0].ID); err != nil {
		t.Fatalf("отказ: %v", err)
	}
	if _, err := g.Invite(ctx, members[0].UserID, second); err != nil {
		t.Errorf("после освободившегося места: %v", err)
	}
}

func TestReturningMemberKeepsHisHistory(t *testing.T) {
	// Ушедшему возвращается его прежняя строка: на неё ссылаются его траты,
	// и вторая развела бы одного человека на две строки в отчёте.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	if _, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[1].ID, Kind: KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", SpentAt: time.Now(),
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	if err := g.Leave(ctx, members[1].UserID); err != nil {
		t.Fatalf("выход: %v", err)
	}
	inv, err := g.Invite(ctx, members[0].UserID, members[1].UserID)
	if err != nil {
		t.Fatalf("приглашение вернувшегося: %v", err)
	}
	back, err := s.AcceptInvite(ctx, members[1].UserID, inv.ID)
	if err != nil {
		t.Fatalf("возвращение: %v", err)
	}

	if back.ID != members[1].ID {
		t.Errorf("вернувшемуся выдана строка %d, ожидалась прежняя %d", back.ID, members[1].ID)
	}
	all, _ := g.AllMembers(ctx)
	if len(all) != 2 {
		t.Errorf("участников в истории %d, ожидалось двое — человек не должен раздваиваться", len(all))
	}
}
