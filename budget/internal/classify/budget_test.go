package classify

import (
	"context"
	"errors"
	"testing"

	"budget/internal/storage"
)

// fakeUsageStore — расход, который можно задать вручную.
type fakeUsageStore struct {
	usage storage.MonthUsage
	err   error
	calls int
}

func (f *fakeUsageStore) MonthlyUsage(context.Context) (storage.MonthUsage, error) {
	f.calls++
	return f.usage, f.err
}

func TestBudgetStopsAtCeiling(t *testing.T) {
	store := &fakeUsageStore{usage: storage.MonthUsage{TotalTokens: 2_000_000}}
	b := NewBudget(2_000_000, store, nil, quietLog())

	if b.Allow(context.Background()) {
		t.Error("на потолке сетевые вызовы должны прекращаться (§7)")
	}
}

func TestBudgetAllowsBelowCeiling(t *testing.T) {
	store := &fakeUsageStore{usage: storage.MonthUsage{TotalTokens: 840_000}}
	b := NewBudget(2_000_000, store, nil, quietLog())

	if !b.Allow(context.Background()) {
		t.Error("ожидаемый месячный расход не должен упираться в потолок")
	}
}

func TestBudgetWarnsOnceAtEightyPercent(t *testing.T) {
	var sent []string
	store := &fakeUsageStore{usage: storage.MonthUsage{TotalTokens: 1_600_000}}
	b := NewBudget(2_000_000, store, func(text string) { sent = append(sent, text) }, quietLog())

	for i := 0; i < 3; i++ {
		if !b.Allow(context.Background()) {
			t.Fatal("на 80% работа не прекращается, шлётся только уведомление")
		}
	}
	if len(sent) != 1 {
		t.Fatalf("отправлено уведомлений %d, ожидалось одно за месяц (§7)", len(sent))
	}
}

func TestBudgetWarnsAgainInNewMonth(t *testing.T) {
	var sent []string
	store := &fakeUsageStore{usage: storage.MonthUsage{TotalTokens: 1_600_000}}
	b := NewBudget(2_000_000, store, func(text string) { sent = append(sent, text) }, quietLog())

	c := &clock{}
	b.now = c.now
	c.t = c.t.AddDate(2026, 6, 0) // июль
	b.Allow(context.Background())
	c.t = c.t.AddDate(0, 1, 0) // август
	b.Allow(context.Background())

	if len(sent) != 2 {
		t.Errorf("отправлено %d уведомлений, в новом месяце ожидалось новое", len(sent))
	}
}

func TestBudgetDisabledWhenLimitZero(t *testing.T) {
	store := &fakeUsageStore{usage: storage.MonthUsage{TotalTokens: 999_999_999}}
	b := NewBudget(0, store, nil, quietLog())

	if !b.Allow(context.Background()) {
		t.Error("нулевой потолок означает выключенную защиту")
	}
	if store.calls != 0 {
		t.Error("при выключенной защите расход читать незачем")
	}
}

func TestBudgetSurvivesStorageError(t *testing.T) {
	store := &fakeUsageStore{err: errors.New("база отвалилась")}
	b := NewBudget(2_000_000, store, nil, quietLog())

	if !b.Allow(context.Background()) {
		t.Error("нечитаемый расход не повод глушить разбор — лимит не про экономию")
	}
}
