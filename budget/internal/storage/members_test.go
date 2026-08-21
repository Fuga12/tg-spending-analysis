package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestLeaveKeepsHistory(t *testing.T) {
	// Строка ушедшего остаётся: на неё ссылаются его траты, и без неё
	// история группы теряет плательщика.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	if _, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[1].ID, Recipients: memberIDs(members, 1), Kind: KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", SpentAt: time.Now(),
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	if err := g.Leave(ctx, members[1].UserID); err != nil {
		t.Fatalf("выход: %v", err)
	}

	active, _ := g.Members(ctx)
	if len(active) != 1 || active[0].ID != members[0].ID {
		t.Errorf("действующих участников = %+v, ожидался один", active)
	}
	all, _ := g.AllMembers(ctx)
	if len(all) != 2 {
		t.Fatalf("в истории участников %d, ожидалось двое", len(all))
	}
	if all[1].LeftAt == nil {
		t.Error("у ушедшего должна стоять дата ухода")
	}

	// Траты никуда не делись и по-прежнему подписаны им.
	now := time.Now()
	rows, err := g.Expenses(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("расходы: %v", err)
	}
	if len(rows) != 1 || rows[0].PayerMemberID != members[1].ID {
		t.Errorf("расходы = %+v, ожидалась трата ушедшего", rows)
	}

	// Место в группе освободилось.
	guest := newcomer(t, s, 50, "Гость")
	if _, err := g.AddMember(ctx, guest, RoleMember); err != nil {
		t.Errorf("после ухода место должно освободиться: %v", err)
	}
}

func TestLastAdminCannotLeave(t *testing.T) {
	// Группа без администратора никому не принадлежит: некому ни звать,
	// ни исключать.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	if err := g.Leave(ctx, members[0].UserID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("выход последнего администратора = %v, ожидалось ErrLastAdmin", err)
	}

	// А с двумя администраторами — можно.
	if err := g.SetRole(ctx, members[0].UserID, members[1].ID, RoleAdmin); err != nil {
		t.Fatalf("повышение: %v", err)
	}
	if err := g.Leave(ctx, members[0].UserID); err != nil {
		t.Errorf("выход при втором администраторе: %v", err)
	}
}

func TestLastAdminCannotDemoteHimself(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	if err := g.SetRole(ctx, members[0].UserID, members[0].ID, RoleMember); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("самороспуск = %v, ожидалось ErrLastAdmin", err)
	}

	// Передать полномочия и разжаловать себя — можно: администратор остаётся.
	if err := g.SetRole(ctx, members[0].UserID, members[1].ID, RoleAdmin); err != nil {
		t.Fatalf("повышение: %v", err)
	}
	if err := g.SetRole(ctx, members[0].UserID, members[0].ID, RoleMember); err != nil {
		t.Errorf("разжалование при втором администраторе: %v", err)
	}
}

func TestRemoveMemberIsForAdminsOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 3)

	if err := g.RemoveMember(ctx, members[1].UserID, members[2].ID); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("исключение обычным участником = %v, ожидалось ErrNotAdmin", err)
	}
	if err := g.RemoveMember(ctx, 999, members[2].ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("исключение посторонним = %v, ожидалось ErrNotMember", err)
	}

	if err := g.RemoveMember(ctx, members[0].UserID, members[2].ID); err != nil {
		t.Fatalf("исключение администратором: %v", err)
	}
	active, _ := g.Members(ctx)
	if len(active) != 2 {
		t.Errorf("действующих участников %d, ожидалось двое", len(active))
	}
}

func TestAdminRemovesHimselfThroughLeave(t *testing.T) {
	// «Выйти» и «выгнать» — разные действия, и подтверждение у них разное.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	if err := g.SetRole(ctx, members[0].UserID, members[1].ID, RoleAdmin); err != nil {
		t.Fatalf("повышение: %v", err)
	}
	if err := g.RemoveMember(ctx, members[0].UserID, members[0].ID); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("исключение самого себя = %v, для этого есть Leave", err)
	}
	if err := g.Leave(ctx, members[0].UserID); err != nil {
		t.Errorf("выход: %v", err)
	}
}

func TestRemovedMemberCannotBeRemovedTwice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	if err := g.RemoveMember(ctx, members[0].UserID, members[1].ID); err != nil {
		t.Fatalf("исключение: %v", err)
	}
	if err := g.RemoveMember(ctx, members[0].UserID, members[1].ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("повторное исключение = %v, ожидалось ErrNotMember", err)
	}
}

func TestRolesDoNotLeakBetweenGroups(t *testing.T) {
	// Администратор своей группы — никто в чужой.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	other := newcomer(t, s, 99, "Чужой")
	otherGroup, otherAdmin, err := s.CreateGroup(ctx, "Другая", other)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}
	og := s.ForGroup(otherGroup.ID)

	if err := og.RemoveMember(ctx, members[0].UserID, otherAdmin.ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("исключение в чужой группе = %v, ожидалось ErrNotMember", err)
	}
	if err := g.RemoveMember(ctx, members[0].UserID, otherAdmin.ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("исключение чужого участника = %v, ожидалось ErrNotMember", err)
	}
	if _, err := og.Invite(ctx, members[0].UserID, 50); !errors.Is(err, ErrNotMember) {
		t.Errorf("приглашение в чужую группу = %v, ожидалось ErrNotMember", err)
	}
}

func TestSetRoleRejectsNonsense(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	if err := g.SetRole(ctx, members[0].UserID, members[1].ID, "владелец"); !errors.Is(err, ErrBadRole) {
		t.Errorf("выдуманная роль = %v, ожидалось ErrBadRole", err)
	}
	if err := g.SetRole(ctx, members[0].UserID, 12345, RoleAdmin); !errors.Is(err, ErrNotMember) {
		t.Errorf("роль несуществующему = %v, ожидалось ErrNotMember", err)
	}
	// Повторное назначение той же роли — не ошибка: кнопку могли нажать дважды.
	if err := g.SetRole(ctx, members[0].UserID, members[1].ID, RoleMember); err != nil {
		t.Errorf("та же роль повторно: %v", err)
	}
}
