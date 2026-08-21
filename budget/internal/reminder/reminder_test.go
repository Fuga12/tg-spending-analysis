package reminder

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/config"
	"budget/internal/storage"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// spySender запоминает, кому что ушло.
type spySender struct{ sent map[int64]string }

func (s *spySender) Send(userID int64, text string) error {
	if s.sent == nil {
		s.sent = map[int64]string{}
	}
	s.sent[userID] = text
	return nil
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

// testGroup заводит группу и возвращает её хранилище вместе с участниками
// в порядке telegram id.
func testGroup(t *testing.T, s *storage.Store, ids ...int64) (*storage.GroupStore, []storage.Member) {
	t.Helper()
	ctx := context.Background()

	for _, id := range ids {
		if err := s.EnsureUser(ctx, id, "Тест"); err != nil {
			t.Fatalf("пользователь %d: %v", id, err)
		}
	}
	gr, admin, err := s.CreateGroup(ctx, "Тест", ids[0])
	if err != nil {
		t.Fatalf("группа: %v", err)
	}
	g := s.ForGroup(gr.ID)

	members := []storage.Member{admin}
	for _, id := range ids[1:] {
		m, err := g.AddMember(ctx, id, storage.RoleMember)
		if err != nil {
			t.Fatalf("участник %d: %v", id, err)
		}
		members = append(members, m)
	}
	return g, members
}

// record кладёт трату от имени участника — всё, что нужно напоминанию.
func record(t *testing.T, g *storage.GroupStore, m storage.Member, spentAt time.Time) int64 {
	t.Helper()
	id, err := g.InsertTransaction(context.Background(), storage.Transaction{
		PayerMemberID: m.ID, Recipients: []int64{m.ID}, Kind: classify.KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", SpentAt: spentAt,
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	return id
}

func TestNotifyOnlySilentUsers(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	const talker, silent = int64(901), int64(902)
	g, members := testGroup(t, store, talker, silent)

	now := time.Now()
	record(t, g, members[0], now)

	sender := &spySender{}
	// Whitelist здесь ни при чём: кому напоминать, решает состав групп.
	// При открытом боте список пуст, и по нему не ушло бы никому.
	r := New(&config.Config{TZ: time.UTC, ReminderAt: "21:00"}, store, sender, quietLog())

	r.Notify(ctx, now)

	if _, ok := sender.sent[talker]; ok {
		t.Error("тому, кто сегодня уже писал, напоминание не нужно (§12)")
	}
	if sender.sent[silent] != text {
		t.Errorf("молчуну отправлено %q, ожидалось %q", sender.sent[silent], text)
	}
}

func TestNotifyCountsTodaysActivityNotSpentDate(t *testing.T) {
	// Человек сегодня вечером записал вчерашнюю трату: ботом он пользовался,
	// дёргать его незачем.
	store := testStore(t)
	ctx := context.Background()

	const userID = int64(903)
	g, members := testGroup(t, store, userID)

	now := time.Now()
	record(t, g, members[0], now.AddDate(0, 0, -1))

	sender := &spySender{}
	r := New(&config.Config{
		TZ: time.UTC, ReminderAt: "21:00",
	}, store, sender, quietLog())

	r.Notify(ctx, now)

	if _, ok := sender.sent[userID]; ok {
		t.Error("тому, кто сегодня уже записывал траты, напоминание не нужно")
	}
}

func TestNotifyIgnoresYesterdaysActivity(t *testing.T) {
	// А вот запись, сделанная вчера, сегодняшнее напоминание не отменяет.
	store := testStore(t)
	ctx := context.Background()

	const userID = int64(904)
	g, members := testGroup(t, store, userID)

	now := time.Now()
	id := record(t, g, members[0], now.AddDate(0, 0, -1))
	if _, err := store.Pool().Exec(ctx,
		`update transactions set created_at = now() - interval '1 day' where id = $1`, id); err != nil {
		t.Fatalf("сдвиг даты записи: %v", err)
	}

	sender := &spySender{}
	r := New(&config.Config{
		TZ: time.UTC, ReminderAt: "21:00",
	}, store, sender, quietLog())

	r.Notify(ctx, now)

	if sender.sent[userID] != text {
		t.Error("вчерашняя активность не отменяет сегодняшнее напоминание")
	}
}

func TestStartValidatesSchedule(t *testing.T) {
	r := New(&config.Config{
		TZ: time.UTC, ReminderAt: "25:99",
	}, nil, &spySender{}, quietLog())

	if err := r.Start(); err == nil {
		t.Error("кривое время должно валить старт напоминания")
	}
}

func TestNotifySkipsPeopleWithoutGroup(t *testing.T) {
	// Человек нашёл бота, но ни в какой группе не состоит: записывать ему
	// некуда, и напоминать не о чем.
	store := testStore(t)
	ctx := context.Background()

	const inGroup, alone = int64(905), int64(906)
	testGroup(t, store, inGroup)
	if err := store.EnsureUser(ctx, alone, "Одиночка"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}

	sender := &spySender{}
	r := New(&config.Config{TZ: time.UTC, ReminderAt: "21:00"}, store, sender, quietLog())

	r.Notify(ctx, time.Now())

	if sender.sent[inGroup] != text {
		t.Errorf("участнику группы напоминание не пришло: %+v", sender.sent)
	}
	if _, ok := sender.sent[alone]; ok {
		t.Error("человеку без группы напоминать не о чем")
	}
}
