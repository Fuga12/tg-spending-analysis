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

// staleAfter — сколько запись имеет право провисеть в очереди. Дальше она
// закрывается «Прочим»: модель, которая устойчиво отвечает не по схеме или
// не отвечает вовсе, иначе перезапрашивалась бы каждые десять минут вечно —
// ровно тот цикл в воркере, от которого защищает §7.
const staleAfter = 2 * time.Hour

// Backfill — периодический добор непроклассифицированных транзакций.
type Backfill struct {
	store      *storage.Store
	classifier *classify.Service
	breaker    *classify.Breaker
	budget     *classify.Budget
	log        *slog.Logger
	now        func() time.Time // подменяется в тестах
}

func New(store *storage.Store, classifier *classify.Service, breaker *classify.Breaker,
	budget *classify.Budget, log *slog.Logger) *Backfill {
	return &Backfill{
		store: store, classifier: classifier, breaker: breaker,
		budget: budget, log: log, now: time.Now,
	}
}

// Tick — один проход. Если breaker открыт или бюджет исчерпан, тик
// пропускается целиком, без единого сетевого вызова (§12).
func (w *Backfill) Tick(ctx context.Context) {
	// Паника на одной записи не должна ронять процесс вместе с ботом.
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("паника в воркере добора", "panic", r)
		}
	}()

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

// process доводит одну запись до разобранного состояния.
func (w *Backfill) process(ctx context.Context, tx storage.Transaction) {
	// Запись принадлежит группе — и словарь, и категории берутся у неё.
	g := w.store.ForGroup(tx.GroupID)

	res, err := w.classifier.Classify(ctx, classify.Scope{
		UserID: tx.PayerUserID, Dict: g, Usage: g,
	}, tx.RawText)
	if err != nil {
		w.log.Warn("добор не удался", "err", err, "tx", tx.ID)
		w.markOther(ctx, g, tx)
		return
	}

	if res.Source == classify.SourceDegraded {
		if res.Reason == classify.ReasonUnusable || w.stale(tx) {
			// Разобрать не выходит и не выйдет — закрываем, чтобы запись
			// не возвращалась каждые десять минут.
			w.log.Warn("запись закрыта без разбора", "tx", tx.ID, "причина", res.Reason)
			w.markOther(ctx, g, tx)
			return
		}
		w.log.Info("добор отложен", "tx", tx.ID, "причина", res.Reason)
		return
	}

	item, ok := matchByAmount(res.Items, tx)
	if !ok {
		w.log.Warn("модель не нашла в сообщении эту сумму",
			"tx", tx.ID, "сумма", tx.Amount.String(), "raw_text", tx.RawText)
		w.markOther(ctx, g, tx)
		return
	}

	updated, err := g.ApplyClassification(ctx, tx.ID,
		item.CategoryID, item.Kind, spentAt(tx, item.DaysAgo))
	if err != nil {
		w.log.Error("не проставил разбор", "err", err, "tx", tx.ID)
		return
	}
	if !updated {
		// Запись успели удалить, пока мы ходили в модель.
		return
	}
	w.remember(ctx, g, tx.PayerUserID, item)

	// Деградированный путь сохраняет только первую сумму (§8). Остальные
	// траты того же сообщения дошли до нас в raw_text — теперь, когда модель
	// ответила, их надо записать, иначе они потеряны навсегда.
	w.saveMissing(ctx, g, tx, res.Items, item)

	w.log.Info("категория добрана", "tx", tx.ID, "вид", item.Kind)
}

// saveMissing дописывает траты из того же сообщения, которых не хватало.
func (w *Backfill) saveMissing(ctx context.Context, g *storage.GroupStore,
	tx storage.Transaction, items []classify.Item, applied classify.Item) {
	for i, item := range items {
		if item.Amount.Equal(applied.Amount) && item.Description == applied.Description {
			continue
		}
		id, err := g.InsertTransaction(ctx, storage.Transaction{
			PayerMemberID: tx.PayerMemberID,
			Recipients:    recipientsOf(tx.PayerMemberID, item.Kind),
			Kind:          item.Kind,
			Amount:        item.Amount,
			Description:   item.Description,
			CategoryID:    item.CategoryID,
			RawText:       tx.RawText,
			SpentAt:       spentAt(tx, item.DaysAgo),
		})
		if err != nil {
			w.log.Error("не записал добранную трату", "err", err, "tx", tx.ID, "элемент", i)
			continue
		}
		w.remember(ctx, g, tx.PayerUserID, item)
		w.log.Info("дописана трата из того же сообщения",
			"tx", id, "исходная", tx.ID, "сумма", item.Amount.String())
	}
}

// recipientsOf — на кого записать трату. Пока всегда на плательщика: модель
// получателя не определяет, а «на всю группу» — это утверждение, которого
// никто не делал (фаза 2). У перевода и дохода получателя нет вовсе.
func recipientsOf(payerMemberID int64, kind string) []int64 {
	if kind != classify.KindExpense {
		return nil
	}
	return []int64{payerMemberID}
}

// remember кладёт слова в личный словарь. Только расходы: быстрый путь всегда
// собирает expense, и запомненный доход во второй раз стал бы тратой.
func (w *Backfill) remember(ctx context.Context, g *storage.GroupStore, userID int64, item classify.Item) {
	if item.CategoryID == nil || item.Kind != classify.KindExpense {
		return
	}
	for _, word := range item.Words {
		if err := g.UpsertWord(ctx, userID, word, *item.CategoryID,
			nil, storage.SourceLLM); err != nil {
			w.log.Warn("не запомнил слово", "err", err, "word", word)
		}
	}
}

// markOther закрывает запись категорией «Прочее»: разобрать её не вышло,
// но и висеть в очереди вечно она не должна.
func (w *Backfill) markOther(ctx context.Context, g *storage.GroupStore, tx storage.Transaction) {
	cats, err := g.Categories(ctx)
	if err != nil {
		w.log.Error("не прочитал категории", "err", err)
		return
	}
	other := classify.FallbackCategory(cats)
	if other == nil {
		// Группа удалила категорию-свалку. Оставить запись в очереди значит
		// запрашивать модель по ней каждые десять минут вечно.
		w.log.Error("в группе нет категории для неразобранного", "group", tx.GroupID, "tx", tx.ID)
		return
	}
	// Помечаем на проверку: категорию выбрал не человек и не модель,
	// а мы сами, лишь бы запись не висела в очереди вечно.
	if _, err := g.MarkForReview(ctx, tx.ID, other.ID); err != nil {
		w.log.Error("не закрыл запись", "err", err, "tx", tx.ID)
	}
}

func (w *Backfill) stale(tx storage.Transaction) bool {
	return w.now().Sub(tx.CreatedAt) > staleAfter
}

// spentAt переносит дату траты на days_ago назад от момента записи.
func spentAt(tx storage.Transaction, daysAgo int) time.Time {
	base := tx.CreatedAt
	if base.IsZero() {
		base = tx.SpentAt
	}
	return base.AddDate(0, 0, -daysAgo)
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

// Run гоняет добор каждые period, пока жив контекст. Закрывает done, когда
// последний тик договорил: гасить пул БД раньше нельзя.
func (w *Backfill) Run(ctx context.Context, period time.Duration, done chan<- struct{}) {
	defer close(done)

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
