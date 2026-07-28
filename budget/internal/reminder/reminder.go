// Package reminder напоминает про траты — но только тем, кто сегодня молчал.
//
// Бот, который пишет каждый вечер независимо от поведения, отправляется
// в mute за неделю (§12).
package reminder

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"budget/internal/config"
	"budget/internal/report"
	"budget/internal/storage"
)

const text = "Что сегодня тратил?"

// Sender — как отправить сообщение пользователю.
type Sender interface {
	Send(userID int64, text string) error
}

// Reminder — вечернее напоминание по крону.
type Reminder struct {
	cfg    *config.Config
	store  *storage.Store
	sender Sender
	log    *slog.Logger
	cron   *cron.Cron
}

func New(cfg *config.Config, store *storage.Store, sender Sender, log *slog.Logger) *Reminder {
	return &Reminder{cfg: cfg, store: store, sender: sender, log: log}
}

// Start заводит крон на REMINDER_AT в таймзоне TZ.
func (r *Reminder) Start() error {
	hour, minute, err := config.ParseHHMM(r.cfg.ReminderAt)
	if err != nil {
		return err
	}

	r.cron = cron.New(cron.WithLocation(r.cfg.TZ))
	spec := fmt.Sprintf("%d %d * * *", minute, hour)
	if _, err := r.cron.AddFunc(spec, r.run); err != nil {
		return err
	}
	r.cron.Start()
	r.log.Info("напоминание заведено", "время", r.cfg.ReminderAt, "tz", r.cfg.TZ.String())
	return nil
}

// Stop останавливает крон и ждёт, пока договорит запущенное задание.
func (r *Reminder) Stop() {
	if r.cron == nil {
		return
	}
	<-r.cron.Stop().Done()
}

func (r *Reminder) run() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r.Notify(ctx, time.Now())
}

// Notify шлёт напоминание тем, у кого сегодня нет ни одной записи.
func (r *Reminder) Notify(ctx context.Context, now time.Time) {
	from, to := report.DayRange(now, r.cfg.TZ)

	for _, userID := range r.cfg.AllowedUserIDs {
		has, err := r.store.HasTransactions(ctx, userID, from, to)
		if err != nil {
			r.log.Error("не проверил траты за день", "err", err, "user_id", userID)
			continue
		}
		if has {
			continue
		}
		if err := r.sender.Send(userID, text); err != nil {
			r.log.Warn("не отправил напоминание", "err", err, "user_id", userID)
		}
	}
}
