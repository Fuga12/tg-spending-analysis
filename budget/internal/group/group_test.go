package group

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"budget/internal/storage"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// spyNotifier запоминает, о чём уведомили, и умеет ломаться по требованию.
type spyNotifier struct {
	got  []storage.Invite
	fail error
}

func (s *spyNotifier) InviteReceived(inv storage.Invite) error {
	s.got = append(s.got, inv)
	return s.fail
}

func testStore(t *testing.T) *storage.Store {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан — интеграционные тесты пропущены")
	}
	// База одна на все пакеты: гонять их параллельно нельзя, они чистят
	// таблицы друг у друга. Запускать через make test-db (там -p 1).
	ctx := context.Background()
	if err := storage.Migrate(ctx, dsn); err != nil {
		t.Fatalf("миграции: %v", err)
	}
	s, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(s.Close)

	if _, err := s.Pool().Exec(ctx, `
		truncate transactions, tx_recipients, word_map, llm_usage,
		         categories, invites, members, groups, users
		restart identity cascade`); err != nil {
		t.Fatalf("очистка: %v", err)
	}
	return s
}

// setup заводит группу с администратором и человека, которого можно позвать.
func setup(t *testing.T, notify Notifier) (*Service, *storage.Store, storage.Group, storage.Member, int64) {
	t.Helper()
	ctx := context.Background()
	store := testStore(t)

	for id, name := range map[int64]string{1: "Илья", 2: "Аня"} {
		if err := store.EnsureUser(ctx, id, name); err != nil {
			t.Fatalf("пользователь %d: %v", id, err)
		}
	}
	svc := New(store, notify, quietLog())

	g, admin, err := svc.Create(ctx, "Тест", 1)
	if err != nil {
		t.Fatalf("создание группы: %v", err)
	}
	return svc, store, g, admin, 2
}

func TestInviteNotifiesTheInvitee(t *testing.T) {
	// Приглашение, о котором никого не уведомили, невидимо: человек, которого
	// зовут, в группе ещё нет и в приложение сам не пойдёт.
	spy := &spyNotifier{}
	svc, _, g, admin, guest := setup(t, spy)
	ctx := context.Background()

	inv, err := svc.Invite(ctx, g.ID, admin.UserID, guest)
	if err != nil {
		t.Fatalf("приглашение: %v", err)
	}

	if len(spy.got) != 1 {
		t.Fatalf("уведомлений %d, ожидалось одно", len(spy.got))
	}
	if spy.got[0].ID != inv.ID || spy.got[0].InviteeUserID != guest {
		t.Errorf("уведомили о %+v, ожидалось приглашение %d для %d", spy.got[0], inv.ID, guest)
	}
	// Уведомлению нужны имена: «Илья зовёт тебя в „Тест“».
	if spy.got[0].InviterName != "Илья" || spy.got[0].GroupName != "Тест" {
		t.Errorf("уведомление = %+v, ожидались имена приглашающего и группы", spy.got[0])
	}
}

func TestFailedNotificationDoesNotCancelInvite(t *testing.T) {
	// Человек мог заблокировать бота. Приглашение уже записано и остаётся
	// действительным — он увидит его в приложении.
	spy := &spyNotifier{fail: errors.New("бот заблокирован")}
	svc, store, g, admin, guest := setup(t, spy)
	ctx := context.Background()

	inv, err := svc.Invite(ctx, g.ID, admin.UserID, guest)
	if err != nil {
		t.Fatalf("приглашение не должно отменяться из-за уведомления: %v", err)
	}

	pending, err := store.PendingInvites(ctx, guest)
	if err != nil {
		t.Fatalf("открытые приглашения: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != inv.ID {
		t.Errorf("открытых приглашений %+v, ожидалось одно", pending)
	}
}

func TestRejectedInviteDoesNotNotify(t *testing.T) {
	// Обычный участник звать не вправе — и беспокоить человека этим тоже.
	spy := &spyNotifier{}
	svc, _, g, admin, guest := setup(t, spy)
	ctx := context.Background()

	if _, err := svc.Invite(ctx, g.ID, guest, admin.UserID); err == nil {
		t.Fatal("приглашение от постороннего должно отказываться")
	}
	if len(spy.got) != 0 {
		t.Errorf("уведомлений %d, при отказе их быть не должно", len(spy.got))
	}
}

func TestAcceptThroughService(t *testing.T) {
	spy := &spyNotifier{}
	svc, _, g, admin, guest := setup(t, spy)
	ctx := context.Background()

	inv, err := svc.Invite(ctx, g.ID, admin.UserID, guest)
	if err != nil {
		t.Fatalf("приглашение: %v", err)
	}
	m, err := svc.Accept(ctx, guest, inv.ID)
	if err != nil {
		t.Fatalf("принятие: %v", err)
	}
	if m.GroupID != g.ID {
		t.Errorf("группа = %d, ожидалась %d", m.GroupID, g.ID)
	}

	// И обратно: выйти можно, пока в группе остаётся администратор.
	if err := svc.Leave(ctx, g.ID, guest); err != nil {
		t.Errorf("выход: %v", err)
	}
}
