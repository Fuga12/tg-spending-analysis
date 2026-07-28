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
	if _, err := s.Pool().Exec(ctx, `truncate transactions restart identity`); err != nil {
		t.Fatalf("очистка: %v", err)
	}
	return s
}

func TestNotifyOnlySilentUsers(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	const talker, silent = int64(901), int64(902)
	for _, id := range []int64{talker, silent} {
		if err := store.UpsertUser(ctx, id, "Тест"); err != nil {
			t.Fatalf("пользователь: %v", err)
		}
	}

	now := time.Now()
	if _, err := store.InsertTransaction(ctx, storage.Transaction{
		PayerID: talker, Beneficiary: classify.BenPayer, Kind: classify.KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", SpentAt: now,
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	sender := &spySender{}
	r := New(&config.Config{
		AllowedUserIDs: []int64{talker, silent},
		TZ:             time.UTC,
		ReminderAt:     "21:00",
	}, store, sender, quietLog())

	r.Notify(ctx, now)

	if _, ok := sender.sent[talker]; ok {
		t.Error("тому, кто сегодня уже писал, напоминание не нужно (§12)")
	}
	if sender.sent[silent] != text {
		t.Errorf("молчуну отправлено %q, ожидалось %q", sender.sent[silent], text)
	}
}

func TestNotifyIgnoresYesterday(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	const userID = int64(903)
	if err := store.UpsertUser(ctx, userID, "Тест"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	now := time.Now()
	if _, err := store.InsertTransaction(ctx, storage.Transaction{
		PayerID: userID, Beneficiary: classify.BenPayer, Kind: classify.KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", SpentAt: now.AddDate(0, 0, -1),
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	sender := &spySender{}
	r := New(&config.Config{
		AllowedUserIDs: []int64{userID}, TZ: time.UTC, ReminderAt: "21:00",
	}, store, sender, quietLog())

	r.Notify(ctx, now)

	if sender.sent[userID] != text {
		t.Error("вчерашняя трата не отменяет сегодняшнее напоминание")
	}
}

func TestStartValidatesSchedule(t *testing.T) {
	r := New(&config.Config{
		AllowedUserIDs: []int64{1}, TZ: time.UTC, ReminderAt: "25:99",
	}, nil, &spySender{}, quietLog())

	if err := r.Start(); err == nil {
		t.Error("кривое время должно валить старт напоминания")
	}
}
