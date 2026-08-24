package classify

import (
	"context"
	"log/slog"

	"budget/internal/storage"
)

// maxRememberedWords — сколько слов правки уезжает в личный кэш. Модель даёт
// описание из 1-3 слов; длиннее — значит описание собрано из сырого текста,
// и учить по нему словарь нельзя.
const maxRememberedWords = 3

// WordWriter — то, что нужно от хранилища группы, чтобы запомнить правку.
type WordWriter interface {
	UpsertWord(ctx context.Context, userID int64, word string, categoryID int32, memberID *int64, source string) error
}

// RememberManual запоминает ручную правку в личном словаре.
//
// Правило одно на бота и на приложение: два интерфейса правят одни и те же
// записи, и если словарь учится по-разному, поведение быстрого пути начинает
// зависеть от того, где нажали кнопку.
func RememberManual(ctx context.Context, w WordWriter, tx storage.Transaction, log *slog.Logger) {
	if tx.CategoryID == nil || tx.Kind != KindExpense {
		return
	}
	// У записи, разобранной вслепую, описание — это весь текст сообщения.
	// Ручная привязка не перезатирается никогда, так что мусор в словаре
	// останется навсегда: лучше не запоминать вовсе.
	if tx.NeedsClassification || tx.NeedsReview {
		return
	}

	words := SignificantWords(tx.Description)
	if len(words) == 0 || len(words) > maxRememberedWords {
		return
	}
	// Получателя словарь помнит вместе с категорией, но только когда он один:
	// «косметика — Уле» запоминается, «продукты на всех» — нет.
	member := RememberedRecipient(Item{Recipients: tx.Recipients})
	for _, word := range words {
		if err := w.UpsertWord(ctx, tx.PayerUserID, word, *tx.CategoryID, member, storage.SourceManual); err != nil {
			log.Warn("не запомнил ручную правку", "err", err, "word", word)
		}
	}
}
