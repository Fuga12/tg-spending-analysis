// Package worker добирает категории записям, которые сохранились
// деградированными: LLM не ответил или был недоступен (§8, §12).
package worker

import (
	"context"
	"log/slog"
	"time"

	"budget/internal/classify"
	"budget/internal/storage"
)

// batchSize — сколько записей берём за тик (§12).
const batchSize = 20

// Backfill — периодический добор непроклассифицированных транзакций.
type Backfill struct {
	store      *storage.Store
	classifier *classify.Service
	breaker    *classify.Breaker
	budget     *classify.Budget
	log        *slog.Logger
}

func New(store *storage.Store, classifier *classify.Service, breaker *classify.Breaker,
	budget *classify.Budget, log *slog.Logger) *Backfill {
	return &Backfill{store: store, classifier: classifier, breaker: breaker, budget: budget, log: log}
}

// Tick — один проход. Если breaker открыт или бюджет исчерпан, тик
// пропускается целиком, без единого сетевого вызова (§12).
func (w *Backfill) Tick(ctx context.Context) {
	if open, until := w.breaker.State(); open {
		w.log.Info("добор пропущен: breaker открыт", "до", until)
		return
	}
	if !w.budget.Allow(ctx) {
		w.log.Info("добор пропущен: месячный потолок токенов исчерпан")
		return
	}

	pending, err := w.store.PendingClassification(ctx, batchSize)
	if err != nil {
		w.log.Error("не смог выбрать записи для добора", "err", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	w.log.Info("добор категорий", "записей", len(pending))
	for _, tx := range pending {
		if ctx.Err() != nil {
			return
		}
		w.process(ctx, tx)
	}
}

// process доводит одну запись до состояния «категория есть».
//
// Флаг снимается в любом случае: иначе запись, которую модель разбирать
// отказывается, возвращалась бы каждые десять минут и жгла бы токены — ровно
// тот сценарий, от которого защищает §7.
func (w *Backfill) process(ctx context.Context, tx storage.Transaction) {
	res, err := w.classifier.Classify(ctx, tx.PayerID, tx.RawText)
	if err != nil {
		w.log.Warn("добор не удался", "err", err, "tx", tx.ID)
		w.markOther(ctx, tx)
		return
	}
	if res.Source == classify.SourceDegraded && res.Reason != classify.ReasonUnusable {
		// Сети снова не хватило — запись остаётся в очереди до следующего раза.
		w.log.Info("добор отложен", "tx", tx.ID, "причина", res.Reason)
		return
	}

	item, ok := matchByAmount(res.Items, tx)
	if !ok || item.CategoryID == nil {
		w.log.Warn("модель не дала категорию для записи", "tx", tx.ID, "raw_text", tx.RawText)
		w.markOther(ctx, tx)
		return
	}

	if _, err := w.store.SetCategory(ctx, tx.ID, tx.PayerID, *item.CategoryID); err != nil {
		w.log.Error("не проставил категорию", "err", err, "tx", tx.ID)
		return
	}
	if item.Beneficiary != tx.Beneficiary {
		if _, err := w.store.SetBeneficiary(ctx, tx.ID, tx.PayerID, item.Beneficiary); err != nil {
			w.log.Warn("не проставил бенефициара", "err", err, "tx", tx.ID)
		}
	}
	for _, word := range item.Words {
		if err := w.store.UpsertWord(ctx, tx.PayerID, word, *item.CategoryID,
			item.Beneficiary, storage.SourceLLM); err != nil {
			w.log.Warn("не запомнил слово", "err", err, "word", word)
		}
	}
	w.log.Info("категория добрана", "tx", tx.ID, "категория", *item.CategoryID)
}

// markOther закрывает запись категорией «Прочее»: разобрать её не вышло,
// но и висеть в очереди вечно она не должна.
func (w *Backfill) markOther(ctx context.Context, tx storage.Transaction) {
	cats, err := w.store.Categories(ctx)
	if err != nil {
		w.log.Error("не прочитал категории", "err", err)
		return
	}
	for _, c := range cats {
		if c.Name == classify.CategoryOther {
			if _, err := w.store.SetCategory(ctx, tx.ID, tx.PayerID, c.ID); err != nil {
				w.log.Error("не закрыл запись", "err", err, "tx", tx.ID)
			}
			return
		}
	}
}

// matchByAmount ищет среди разобранного ту трату, которая соответствует
// записи: сообщение могло содержать несколько сумм.
func matchByAmount(items []classify.Item, tx storage.Transaction) (classify.Item, bool) {
	for _, item := range items {
		if item.Amount.Equal(tx.Amount) {
			return item, true
		}
	}
	return classify.Item{}, false
}

// Run гоняет добор каждые period, пока жив контекст.
func (w *Backfill) Run(ctx context.Context, period time.Duration) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Tick(ctx)
		}
	}
}
