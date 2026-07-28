package backup

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type recorder struct{ sent []string }

func (r *recorder) Send(_ int64, text string) error {
	r.sent = append(r.sent, text)
	return nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// dump кладёт в каталог дамп заданного возраста и размера.
func dump(t *testing.T, dir string, age time.Duration, size int) {
	t.Helper()
	name := filepath.Join(dir, "budget-2026-07-29_0400.sql.gz")
	if err := os.WriteFile(name, make([]byte, size), 0o644); err != nil {
		t.Fatalf("дамп: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(name, when, when); err != nil {
		t.Fatalf("время дампа: %v", err)
	}
}

func newWatcher(t *testing.T, dir string) (*Watcher, *recorder) {
	t.Helper()
	rec := &recorder{}
	return New(dir, 36*time.Hour, 42, time.UTC, rec, quietLog()), rec
}

func TestFreshDumpIsSilent(t *testing.T) {
	dir := t.TempDir()
	dump(t, dir, 3*time.Hour, 4096)

	w, rec := newWatcher(t, dir)
	w.Check()

	if len(rec.sent) != 0 {
		t.Errorf("свежий дамп поводов писать не даёт: %v", rec.sent)
	}
}

func TestStaleDumpWarnsAndKeepsWarning(t *testing.T) {
	dir := t.TempDir()
	dump(t, dir, 50*time.Hour, 4096)

	w, rec := newWatcher(t, dir)
	w.Check()
	if len(rec.sent) != 1 {
		t.Fatalf("предупреждений %d, ожидалось одно: %v", len(rec.sent), rec.sent)
	}

	// Пока не починено — напоминаем каждый раз: молчание тут читается
	// как «всё хорошо».
	w.Check()
	if len(rec.sent) != 2 {
		t.Errorf("предупреждений %d, ожидалось два", len(rec.sent))
	}
}

func TestRecoveryIsReported(t *testing.T) {
	dir := t.TempDir()
	dump(t, dir, 50*time.Hour, 4096)

	w, rec := newWatcher(t, dir)
	w.Check()

	dump(t, dir, time.Hour, 4096)
	w.Check()

	if len(rec.sent) != 2 {
		t.Fatalf("сообщений %d, ожидались жалоба и отбой: %v", len(rec.sent), rec.sent)
	}
	if rec.sent[1] != "Бэкапы снова делаются." {
		t.Errorf("второе сообщение = %q, ожидался отбой", rec.sent[1])
	}
	// И больше не повторяемся.
	w.Check()
	if len(rec.sent) != 2 {
		t.Errorf("после отбоя писать не о чем: %v", rec.sent[2:])
	}
}

func TestEmptyDirAndMissingDirDiffer(t *testing.T) {
	empty := t.TempDir()
	w, _ := newWatcher(t, empty)
	if p := w.problem(time.Now()); p == "" {
		t.Error("пустой каталог — это отсутствие бэкапов, о нём надо сказать")
	}

	w, _ = newWatcher(t, filepath.Join(empty, "нет-такого"))
	if p := w.problem(time.Now()); p == "" {
		t.Error("пропавший каталог — тоже беда")
	}
}

func TestTinyDumpIsNotABackup(t *testing.T) {
	dir := t.TempDir()
	dump(t, dir, time.Hour, 100)

	w, _ := newWatcher(t, dir)
	if p := w.problem(time.Now()); p == "" {
		t.Error("дамп в 100 байт свежестью не искупается")
	}
}
