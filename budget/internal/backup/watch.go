// Package backup следит за тем, что бэкапы вообще делаются.
//
// Бэкапы ломаются молча: крон отвалился, кончилось место, сменился пароль
// к базе. Файлы перестают появляться, но никто не замечает — пока однажды
// не понадобится восстановление, и выяснится, что последний дамп полугодовой
// давности. Поэтому за свежестью следит сам бот: он и так работает всегда,
// а значит переживёт и мёртвый крон (§12).
package backup

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/robfig/cron/v3"
)

// Проверка в полдень: ночной бэкап к этому времени давно должен был пройти.
// Если не прошёл — узнаём днём, а не через сутки.
const checkSpec = "0 12 * * *"

// Дамп меньше килобайта — это не бэкап, а пустой файл с правильным именем.
const minSize = 1024

// Sender — как отправить сообщение владельцу.
type Sender interface {
	Send(userID int64, text string) error
}

// Watcher раз в сутки смотрит на каталог с дампами.
type Watcher struct {
	dir     string
	maxAge  time.Duration
	ownerID int64
	sender  Sender
	log     *slog.Logger
	tz      *time.Location

	cron *cron.Cron
	// broken помнит, жаловались ли уже: «снова в порядке» без этого
	// не отправить, а без такого сообщения непонятно, починилось ли.
	broken bool
}

func New(dir string, maxAge time.Duration, ownerID int64, tz *time.Location, sender Sender, log *slog.Logger) *Watcher {
	return &Watcher{dir: dir, maxAge: maxAge, ownerID: ownerID, tz: tz, sender: sender, log: log}
}

// Start заводит ежедневную проверку и делает первую сразу.
func (w *Watcher) Start() error {
	w.cron = cron.New(
		cron.WithLocation(w.tz),
		cron.WithChain(cron.Recover(cron.PrintfLogger(recoverLogger{w.log}))),
	)
	if _, err := w.cron.AddFunc(checkSpec, w.Check); err != nil {
		return err
	}
	w.cron.Start()

	// Первая проверка — при запуске: перезапуск после долгого простоя самый
	// подходящий момент узнать, что бэкапов давно нет.
	w.Check()
	w.log.Info("слежу за бэкапами", "dir", w.dir, "предел", w.maxAge)
	return nil
}

func (w *Watcher) Stop() {
	if w.cron != nil {
		<-w.cron.Stop().Done()
	}
}

// Check сверяет возраст свежего дампа и жалуется владельцу, если что-то не так.
func (w *Watcher) Check() {
	problem := w.problem(time.Now())
	if problem == "" {
		if w.broken {
			w.broken = false
			w.notify("Бэкапы снова делаются.")
		}
		return
	}

	w.log.Warn("бэкапы", "проблема", problem)
	if w.broken {
		// Жалуемся каждый день, пока не починится: бэкапы — не тот случай,
		// когда уместно сказать один раз и замолчать.
		w.notify("Бэкапы всё ещё не в порядке. " + problem)
		return
	}
	w.broken = true
	w.notify("С бэкапами беда. " + problem)
}

// problem возвращает описание неполадки или пустую строку, если всё хорошо.
func (w *Watcher) problem(now time.Time) string {
	newest, size, err := newestDump(w.dir)
	if err != nil {
		return fmt.Sprintf("каталог %s не читается: %v", w.dir, err)
	}
	if newest.IsZero() {
		return fmt.Sprintf("в %s нет ни одного дампа", w.dir)
	}
	if size < minSize {
		return fmt.Sprintf("последний дамп подозрительно мал: %d Б", size)
	}
	if age := now.Sub(newest); age > w.maxAge {
		return fmt.Sprintf("последний дамп сделан %s — это %s назад",
			newest.In(w.tz).Format("02.01 15:04"), humanAge(age))
	}
	return ""
}

// newestDump — время и размер самого свежего дампа в каталоге.
func newestDump(dir string) (time.Time, int64, error) {
	names, err := filepath.Glob(filepath.Join(dir, "budget-*.sql.gz"))
	if err != nil {
		return time.Time{}, 0, err
	}
	// Читаем каталог отдельно: Glob молчит, когда каталога просто нет,
	// а «нет каталога» и «нет дампов» — разные беды с разным лечением.
	if len(names) == 0 {
		if _, err := os.Stat(dir); err != nil {
			return time.Time{}, 0, err
		}
		return time.Time{}, 0, nil
	}
	sort.Strings(names)

	var newest time.Time
	var size int64
	for _, name := range names {
		info, err := os.Stat(name)
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest, size = info.ModTime(), info.Size()
		}
	}
	return newest, size, nil
}

func humanAge(d time.Duration) string {
	if days := int(d.Hours()) / 24; days >= 1 {
		return fmt.Sprintf("%d дн.", days)
	}
	return fmt.Sprintf("%d ч.", int(d.Hours()))
}

func (w *Watcher) notify(text string) {
	if w.ownerID == 0 || w.sender == nil {
		return
	}
	if err := w.sender.Send(w.ownerID, text); err != nil {
		w.log.Warn("не предупредил про бэкапы", "err", err)
	}
}

type recoverLogger struct{ log *slog.Logger }

func (r recoverLogger) Printf(format string, v ...any) {
	r.log.Error("паника в проверке бэкапов", "msg", fmt.Sprintf(format, v...))
}
