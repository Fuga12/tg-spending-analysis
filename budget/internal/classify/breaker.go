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

	// Ответ не по схеме означает, что сервис жив и отвечает: счётчик подряд
	// идущих отказов сервиса такой ответ обнуляет, как и успех.
	alive := err == nil || ErrKind(err) == storage.ErrKindSchema

	if b.probing {
		// Проба отработала. Только успех закрывает breaker — иначе ждём
		// ещё один срок остывания. Сбрасывать probing обязательно: иначе
		// breaker залипнет открытым навсегда.
		b.probing = false
		if err == nil {
			b.failures, b.openUntil = 0, time.Time{}
			b.log.Info("breaker закрыт")
			return
		}
		b.openUntil = b.now().Add(b.cooldown)
		b.log.Warn("пробный запрос не удался, breaker снова открыт",
			"kind", ErrKind(err), "до", b.openUntil)
		return
	}

	if alive {
		if b.failures > 0 || !b.openUntil.IsZero() {
			b.log.Info("breaker закрыт")
		}
		b.failures, b.openUntil = 0, time.Time{}
		return
	}

	if kind := ErrKind(err); kind != storage.ErrKindQuota && kind != storage.ErrKindHTTP {
		return
	}

	b.failures++
	if b.failures >= breakerThreshold && b.openUntil.IsZero() {
		b.openUntil = b.now().Add(b.cooldown)
		b.log.Warn("breaker открыт", "подряд_ошибок", b.failures, "до", b.openUntil)
	}
}

// State отдаёт состояние для команды /лимит (§7). Пока не отработала пробная
// попытка, breaker считается открытым: сеть для остальных всё ещё закрыта.
func (b *Breaker) State() (open bool, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.openUntil.IsZero() {
		return false, time.Time{}
	}
	if b.probing {
		return true, b.openUntil
	}
	if !b.now().Before(b.openUntil) {
		return false, time.Time{}
	}
	return true, b.openUntil
}
