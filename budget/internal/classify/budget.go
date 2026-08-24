package classify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"budget/internal/storage"
)

// warnShare — доля потолка, на которой владельцу уходит разовое уведомление.
const warnShare = 0.8

// UsageStore — источник данных о расходе токенов.
type UsageStore interface {
	MonthlyUsage(ctx context.Context) (storage.MonthUsage, error)
}

// Budget — защитный месячный лимит токенов.
//
// Это защита от бага, а не от расхода: нормальное использование грант не
// выберет и близко. Опасность — цикл в воркере или случайный прогон по всей
// истории, который молча съедает грант за ночь.
type Budget struct {
	limit  int64
	store  UsageStore
	notify func(text string)
	log    *slog.Logger
	now    func() time.Time // подменяется в тестах

	mu            sync.Mutex
	notifiedMonth string // «2026-07», чтобы не слать предупреждение дважды
}

func NewBudget(limit int64, store UsageStore, notify func(string), log *slog.Logger) *Budget {
	return &Budget{limit: limit, store: store, notify: notify, log: log, now: time.Now}
}

// Allow сообщает, можно ли тратить токены. При переходе через 80% потолка
// один раз за месяц уведомляет владельца.
func (b *Budget) Allow(ctx context.Context) bool {
	if b.limit <= 0 {
		return true
	}

	used, err := b.store.MonthlyUsage(ctx)
	if err != nil {
		// Прочитать расход не удалось. Глушить разбор из-за этого не будем:
		// без БД всё равно ничего не сохранить, а лимит — не про экономию.
		b.log.Error("не смог прочитать расход токенов", "err", err)
		return true
	}

	if used.TotalTokens >= b.limit {
		b.log.Warn("месячный потолок токенов исчерпан, сетевых вызовов нет",
			"израсходовано", used.TotalTokens, "потолок", b.limit)
		return false
	}

	if float64(used.TotalTokens) >= float64(b.limit)*warnShare {
		b.warnOnce(used.TotalTokens)
	}
	return true
}

// Used отдаёт расход за текущий месяц — для команды /лимит.
func (b *Budget) Used(ctx context.Context) (storage.MonthUsage, error) {
	return b.store.MonthlyUsage(ctx)
}

// Limit — потолок в токенах.
func (b *Budget) Limit() int64 { return b.limit }

func (b *Budget) warnOnce(used int64) {
	month := b.now().Format("2006-01")

	b.mu.Lock()
	if b.notifiedMonth == month {
		b.mu.Unlock()
		return
	}
	b.notifiedMonth = month
	b.mu.Unlock()

	percent := float64(used) / float64(b.limit) * 100
	b.log.Warn("израсходовано больше 80% потолка токенов", "израсходовано", used, "потолок", b.limit)
	if b.notify != nil {
		b.notify(fmt.Sprintf(
			"⚠️ Израсходовано %d токенов — %.0f%% месячного потолка (%d).\n"+
				"При нормальной работе это сообщение приходить не должно. Посмотри /лимит.",
			used, percent, b.limit))
	}
}
