package classify

import (
	"log/slog"
	"sync"
	"time"

	"budget/internal/storage"
)

// breakerThreshold — сколько подряд ошибок сервиса открывают breaker (§7).
const breakerThreshold = 3

// Breaker гасит сетевые вызовы, когда API стабильно не отвечает.
//
// Назначение — защита от собственных багов и от спама в логи, когда сервис
// просто лежит: пока breaker открыт, ни обработчик сообщений, ни воркер
// добора в сеть не ходят вообще, а сразу уходят в деградированный путь (§7).
type Breaker struct {
	cooldown time.Duration
	log      *slog.Logger
	now      func() time.Time // подменяется в тестах

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probing   bool // выпущена пробная попытка после остывания
}

func NewBreaker(cooldown time.Duration, log *slog.Logger) *Breaker {
	return &Breaker{cooldown: cooldown, log: log, now: time.Now}
}

// Allow сообщает, можно ли делать сетевой вызов. Когда время остывания вышло,
// наружу выпускается ровно одна пробная попытка.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.openUntil.IsZero() {
		return true
	}
	if b.now().Before(b.openUntil) {
		return false
	}
	if b.probing {
		return false
	}
	b.probing = true
	b.log.Info("breaker остыл, пробую один запрос")
	return true
}

// Record сообщает breaker об исходе вызова. Ошибки, к сервису не относящиеся
// (schema, отмена), в счётчик не идут: они не значат, что API недоступен.
func (b *Breaker) Record(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err == nil {
		if b.failures > 0 || !b.openUntil.IsZero() {
			b.log.Info("breaker закрыт")
		}
		b.failures, b.probing, b.openUntil = 0, false, time.Time{}
		return
	}

	switch ErrKind(err) {
	case storage.ErrKindQuota, storage.ErrKindHTTP:
	default:
		return
	}

	b.failures++
	if b.probing {
		// Пробная попытка провалилась — снова закрываемся на полный срок.
		b.probing = false
		b.openUntil = b.now().Add(b.cooldown)
		b.log.Warn("пробный запрос не удался, breaker снова открыт", "до", b.openUntil)
		return
	}
	if b.failures >= breakerThreshold && b.openUntil.IsZero() {
		b.openUntil = b.now().Add(b.cooldown)
		b.log.Warn("breaker открыт", "подряд_ошибок", b.failures, "до", b.openUntil)
	}
}

// State отдаёт состояние для команды /лимит (§7).
func (b *Breaker) State() (open bool, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.openUntil.IsZero() || !b.now().Before(b.openUntil) {
		return false, time.Time{}
	}
	return true, b.openUntil
}
