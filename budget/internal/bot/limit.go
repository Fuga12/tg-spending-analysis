package bot

import (
	"sync"
	"time"
)

// limiter — сколько сообщений в сутки бот разбирает от одного человека.
//
// Счётчик в памяти, а не в базе: он защищает от того, кто долбит бота прямо
// сейчас, а такой сценарий перезапуск процесса и так прерывает. Строка в базе
// на каждое сообщение стоила бы записи в диск ради счёта, который никому,
// кроме этой проверки, не нужен.
//
// Ноль потолка выключает счёт целиком: до открытия бота считать незачем.
type limiter struct {
	limit int
	now   func() time.Time // подменяется в тестах

	mu    sync.Mutex
	seen  map[int64]int
	reset time.Time
}

func newLimiter(limit int, loc *time.Location) *limiter {
	return &limiter{
		limit: limit,
		now:   func() time.Time { return time.Now().In(loc) },
		seen:  map[int64]int{},
	}
}

// allow отмечает сообщение и говорит, разбирать ли его.
//
// Сутки считаются по календарю, а не скользящим окном: «до завтра» человек
// понимает, «через 14 часов от вашего сорокового сообщения» — нет.
func (l *limiter) allow(userID int64) bool {
	if l.limit <= 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if !day.Equal(l.reset) {
		l.seen = map[int64]int{}
		l.reset = day
	}

	if l.seen[userID] >= l.limit {
		return false
	}
	l.seen[userID]++
	return true
}
