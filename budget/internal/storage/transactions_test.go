package storage

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestInsertAndReadTransaction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 3)

	cats, _ := g.Categories(ctx)
	food := categoryID(t, cats, "Продукты")

	spent := time.Now().AddDate(0, 0, -1)
	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID,
		Recipients:    memberIDs(members, 1, 2),
		Kind:          KindExpense,
		Amount:        decimal.RequireFromString("1200.50"),
		Description:   "пятёрочка",
		CategoryID:    &food,
		RawText:       "вчера пятёрочка 1200,50",
		SpentAt:       spent,
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	tx, err := g.Transaction(ctx, id)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if !tx.Amount.Equal(decimal.RequireFromString("1200.50")) {
		t.Errorf("сумма = %s, ожидалось 1200.50", tx.Amount)
	}
	if tx.CategoryName != "Продукты" {
		t.Errorf("категория = %q, ожидались Продукты", tx.CategoryName)
	}
	if tx.SpentAt.Sub(spent).Abs() > time.Second {
		t.Errorf("дата траты = %s, ожидалась %s", tx.SpentAt, spent)
	}
	if tx.NeedsClassification {
		t.Error("флаг «разобрать позже» не должен стоять")
	}
	if len(tx.Recipients) != 2 ||
		tx.Recipients[0] != members[1].ID || tx.Recipients[1] != members[2].ID {
		t.Errorf("получатели = %v, ожидались %v", tx.Recipients, memberIDs(members, 1, 2))
	}
}

func TestEmptyRecipientsMeanWholeGroup(t *testing.T) {
	// Пустой список — это «на всю группу», а не потерянные данные: состав
	// группы меняется, а смысл «общая трата» — нет.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 4)

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID,
		Kind:          KindExpense,
		Amount:        decimal.RequireFromString("1000"),
		Description:   "продукты",
		RawText:       "продукты 1000",
		SpentAt:       time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	tx, err := g.Transaction(ctx, id)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if len(tx.Recipients) != 0 {
		t.Errorf("получатели = %v, ожидался пустой список", tx.Recipients)
	}
}

func TestRecipientFromOtherGroupIsRejected(t *testing.T) {
	// Главная защита от худшего бага мультитенантности: участник чужой группы
	// не может оказаться получателем нашей траты, даже если его id подставили.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	if err := s.EnsureUser(ctx, 99, "Чужой"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	_, outsider, err := s.CreateGroup(ctx, "Другая", 99)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID,
		Recipients:    []int64{members[1].ID, outsider.ID},
		Kind:          KindExpense,
		Amount:        decimal.RequireFromString("500"),
		Description:   "тест",
		RawText:       "тест 500",
		SpentAt:       time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	tx, _ := g.Transaction(ctx, id)
	if len(tx.Recipients) != 1 || tx.Recipients[0] != members[1].ID {
		t.Errorf("получатели = %v, ожидался только свой участник %d", tx.Recipients, members[1].ID)
	}
}

func TestPayerFromOtherGroupIsRejected(t *testing.T) {
	// Тот же запрет на уровне схемы: составной внешний ключ не даёт записать
	// трату на плательщика из чужой группы.
	s := testStore(t)
	ctx := context.Background()
	g, _ := testGroup(t, s, 2)

	if err := s.EnsureUser(ctx, 99, "Чужой"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	_, outsider, err := s.CreateGroup(ctx, "Другая", 99)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}

	_, err = g.InsertTransaction(ctx, Transaction{
		PayerMemberID: outsider.ID,
		Kind:          KindExpense,
		Amount:        decimal.RequireFromString("500"),
		Description:   "тест",
		RawText:       "тест 500",
		SpentAt:       time.Now(),
	})
	if err == nil {
		t.Error("чужой плательщик не должен проходить внешний ключ")
	}
}

func TestDegradedTransactionHasNoCategory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID:       members[0].ID,
		Recipients:          memberIDs(members, 0),
		Kind:                KindExpense,
		Amount:              decimal.RequireFromString("600"),
		Description:         "лимонад",
		RawText:             "600 лимонад",
		NeedsClassification: true,
		SpentAt:             time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка деградированной записи: %v", err)
	}

	tx, err := g.Transaction(ctx, id)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if tx.CategoryID != nil || !tx.NeedsClassification {
		t.Errorf("запись = %+v, ожидались пустая категория и флаг разбора", tx)
	}
}

func TestAnyoneCanEditTransaction(t *testing.T) {
	// Бюджет общий: править чужую запись разрешено любому участнику.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 3)

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID,
		Recipients:    memberIDs(members, 0),
		Kind:          KindExpense,
		Amount:        decimal.RequireFromString("450"),
		Description:   "такси",
		RawText:       "такси 450",
		SpentAt:       time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	ok, err := g.SetRecipients(ctx, id, memberIDs(members, 1, 2))
	if err != nil || !ok {
		t.Fatalf("смена получателей: ok=%v err=%v", ok, err)
	}
	tx, _ := g.Transaction(ctx, id)
	if len(tx.Recipients) != 2 {
		t.Errorf("получатели = %v, ожидались двое", tx.Recipients)
	}

	// Пустой список стирает прежних: трата стала общей.
	if ok, err := g.SetRecipients(ctx, id, nil); err != nil || !ok {
		t.Fatalf("сброс получателей: ok=%v err=%v", ok, err)
	}
	tx, _ = g.Transaction(ctx, id)
	if len(tx.Recipients) != 0 {
		t.Errorf("получатели = %v, ожидался пустой список", tx.Recipients)
	}
}

func TestMarkForReviewClearsQueueButFlagsRecord(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	cats, _ := g.Categories(ctx)
	other := categoryID(t, cats, "Прочее")

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID, Kind: KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", NeedsClassification: true, SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	if ok, err := g.MarkForReview(ctx, id, other); err != nil || !ok {
		t.Fatalf("простановка вслепую: ok=%v err=%v", ok, err)
	}
	tx, _ := g.Transaction(ctx, id)
	if tx.NeedsClassification {
		t.Error("после простановки категории запись должна уйти из очереди")
	}
	if !tx.NeedsReview {
		t.Error("категорию выбрал не человек — запись обязана быть помечена на проверку")
	}

	// Повторно закрывать уже закрытую запись нечего.
	if ok, _ := g.MarkForReview(ctx, id, other); ok {
		t.Error("запись вне очереди не должна перезакрываться")
	}
}

func TestDeleteIsSoftAndReversible(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID, Recipients: memberIDs(members, 0), Kind: KindExpense,
		Amount: decimal.RequireFromString("450"), Description: "такси",
		RawText: "такси 450", SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	if ok, err := g.DeleteTransaction(ctx, id); err != nil || !ok {
		t.Fatalf("удаление: ok=%v err=%v", ok, err)
	}
	if _, err := g.Transaction(ctx, id); err == nil {
		t.Error("удалённая трата не должна читаться")
	}

	// Строка осталась: удаление только мягкое (§3).
	var deleted int
	if err := s.pool.QueryRow(ctx,
		`select count(*) from transactions where id = $1 and deleted_at is not null`, id).Scan(&deleted); err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if deleted != 1 {
		t.Error("строка должна остаться в базе с проставленным deleted_at")
	}

	// Повторное удаление ничего не меняет, а вернуть можно.
	if ok, _ := g.DeleteTransaction(ctx, id); ok {
		t.Error("повторное удаление не должно проходить")
	}
	if ok, err := g.RestoreTransaction(ctx, id); err != nil || !ok {
		t.Fatalf("возврат: ok=%v err=%v", ok, err)
	}
	if _, err := g.Transaction(ctx, id); err != nil {
		t.Errorf("возвращённая трата должна читаться: %v", err)
	}
}

func TestTransactionIsInvisibleFromAnotherGroup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID, Kind: KindExpense,
		Amount: decimal.RequireFromString("450"), Description: "такси",
		RawText: "такси 450", SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	if err := s.EnsureUser(ctx, 99, "Чужой"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	other, _, err := s.CreateGroup(ctx, "Другая", 99)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}
	og := s.ForGroup(other.ID)

	if _, err := og.Transaction(ctx, id); err == nil {
		t.Error("чужая трата не должна читаться")
	}
	if ok, _ := og.DeleteTransaction(ctx, id); ok {
		t.Error("чужую трату не должно быть возможно удалить")
	}
}

func TestAmountMustBePositive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	_, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID, Kind: KindExpense,
		Amount: decimal.Zero, Description: "тест", RawText: "0 тест", SpentAt: time.Now(),
	})
	if err == nil {
		t.Error("нулевая сумма не должна проходить check (amount > 0)")
	}
}

func TestExpensesFiltersPeriodKindAndDeleted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	now := time.Now()
	insert := func(kind string, amount string, spentAt time.Time, deleted bool) int64 {
		t.Helper()
		id, err := g.InsertTransaction(ctx, Transaction{
			PayerMemberID: members[0].ID, Kind: kind,
			Amount: decimal.RequireFromString(amount), Description: "тест",
			RawText: "тест", SpentAt: spentAt,
		})
		if err != nil {
			t.Fatalf("вставка: %v", err)
		}
		if deleted {
			if _, err := g.DeleteTransaction(ctx, id); err != nil {
				t.Fatalf("удаление: %v", err)
			}
		}
		return id
	}

	insert(KindExpense, "1000", now, false)
	insert(KindExpense, "2000", now, true)                    // удалённая
	insert(KindTransfer, "5000", now, false)                  // перевод — не расход
	insert(KindIncome, "90000", now, false)                   // доход — не расход
	insert(KindExpense, "700", now.AddDate(0, 0, -40), false) // другой месяц

	from := now.AddDate(0, 0, -7)
	to := now.AddDate(0, 0, 1)
	rows, err := g.Expenses(ctx, from, to)
	if err != nil {
		t.Fatalf("расходы: %v", err)
	}
	if len(rows) != 1 || !rows[0].Amount.Equal(decimal.RequireFromString("1000")) {
		t.Fatalf("строк %d (%+v), ожидалась одна на 1000 — переводы, доходы, удалённые и чужие месяцы не в счёт", len(rows), rows)
	}
}

func TestExpensesDoNotLeakBetweenGroups(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 1)

	now := time.Now()
	if _, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID, Kind: KindExpense,
		Amount: decimal.RequireFromString("1000"), Description: "тест",
		RawText: "тест", SpentAt: now,
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	if err := s.EnsureUser(ctx, 99, "Чужой"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	other, otherMember, err := s.CreateGroup(ctx, "Другая", 99)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}
	og := s.ForGroup(other.ID)
	if _, err := og.InsertTransaction(ctx, Transaction{
		PayerMemberID: otherMember.ID, Kind: KindExpense,
		Amount: decimal.RequireFromString("55"), Description: "чужое",
		RawText: "чужое", SpentAt: now,
	}); err != nil {
		t.Fatalf("вставка в чужую группу: %v", err)
	}

	from, to := now.AddDate(0, 0, -1), now.AddDate(0, 0, 1)
	rows, err := g.Expenses(ctx, from, to)
	if err != nil {
		t.Fatalf("расходы: %v", err)
	}
	if len(rows) != 1 || !rows[0].Amount.Equal(decimal.RequireFromString("1000")) {
		t.Errorf("расходы группы = %+v, чужие траты в них попасть не должны", rows)
	}
}

func TestPendingClassificationCarriesGroupAndPayer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	cats, _ := g.Categories(ctx)
	food := categoryID(t, cats, "Продукты")

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[1].ID, Kind: KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "лимонад",
		RawText: "600 лимонад", NeedsClassification: true, SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	if _, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID, Kind: KindExpense,
		Amount: decimal.RequireFromString("450"), Description: "такси",
		RawText: "такси 450", CategoryID: &food, SpentAt: time.Now(),
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	pending, err := s.PendingClassification(ctx, 20)
	if err != nil {
		t.Fatalf("выборка: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("добирать нужно только помеченные записи, получено %+v", pending)
	}
	if pending[0].RawText != "600 лимонад" {
		t.Errorf("raw_text = %q — воркеру нужен исходный текст", pending[0].RawText)
	}
	// Воркер обходит очередь всех групп: без group_id ему негде взять
	// категории, а без user_id — личный словарь плательщика.
	if pending[0].GroupID != g.GroupID() {
		t.Errorf("group_id = %d, ожидался %d", pending[0].GroupID, g.GroupID())
	}
	if pending[0].PayerUserID != members[1].UserID {
		t.Errorf("payer user_id = %d, ожидался %d", pending[0].PayerUserID, members[1].UserID)
	}

	// После разбора запись из очереди уходит.
	ok, err := g.ApplyClassification(ctx, id, &food, KindExpense, time.Now())
	if err != nil || !ok {
		t.Fatalf("разбор: ok=%v err=%v", ok, err)
	}
	if pending, _ = s.PendingClassification(ctx, 20); len(pending) != 0 {
		t.Errorf("в очереди осталось %d записей, ожидалось 0", len(pending))
	}
}

func TestApplyClassificationClearsRecipientsOnTransfer(t *testing.T) {
	// Перевод — не трата: получателя у него нет, и оставлять того, кого
	// проставил деградированный путь, нельзя.
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	id, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID, Recipients: memberIDs(members, 0), Kind: KindExpense,
		Amount: decimal.RequireFromString("5000"), Description: "5к",
		RawText: "скинул 5к", NeedsClassification: true, SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	if ok, err := g.ApplyClassification(ctx, id, nil, KindTransfer, time.Now()); err != nil || !ok {
		t.Fatalf("разбор: ok=%v err=%v", ok, err)
	}
	tx, _ := g.Transaction(ctx, id)
	if tx.Kind != KindTransfer {
		t.Errorf("вид = %q, ожидался перевод", tx.Kind)
	}
	if len(tx.Recipients) != 0 {
		t.Errorf("получатели перевода = %v, ожидался пустой список", tx.Recipients)
	}
}

func TestHasRecordedOnLooksAtPerson(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, members := testGroup(t, s, 2)

	now := time.Now()
	if _, err := g.InsertTransaction(ctx, Transaction{
		PayerMemberID: members[0].ID, Kind: KindExpense,
		Amount: decimal.RequireFromString("100"), Description: "тест",
		RawText: "тест", SpentAt: now,
	}); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	has, err := s.HasRecordedOn(ctx, members[0].UserID, from, to)
	if err != nil || !has {
		t.Errorf("писавший сегодня = %v (%v), ожидалось true", has, err)
	}
	has, err = s.HasRecordedOn(ctx, members[1].UserID, from, to)
	if err != nil || has {
		t.Errorf("молчавший сегодня = %v (%v), ожидалось false", has, err)
	}
}

func TestPendingClassificationGoesRoundByGroup(t *testing.T) {
	// Группа, где сегодня записали много трат при лежащем API, не должна
	// забивать батч целиком: в маленькой группе человек иначе ждал бы свою
	// категорию часами.
	s := testStore(t)
	ctx := context.Background()
	busy, busyMembers := testGroup(t, s, 1)

	if err := s.EnsureUser(ctx, 99, "Тихий"); err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	quietGroup, quietMember, err := s.CreateGroup(ctx, "Тихая", 99)
	if err != nil {
		t.Fatalf("вторая группа: %v", err)
	}
	quiet := s.ForGroup(quietGroup.ID)

	pending := func(g *GroupStore, member Member, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := g.InsertTransaction(ctx, Transaction{
				PayerMemberID: member.ID, Kind: KindExpense,
				Amount: decimal.RequireFromString("100"), Description: "тест",
				RawText: "тест 100", NeedsClassification: true, SpentAt: time.Now(),
			}); err != nil {
				t.Fatalf("вставка: %v", err)
			}
		}
	}

	// Шумная группа записала десять трат раньше, тихая — одну после.
	pending(busy, busyMembers[0], 10)
	pending(quiet, quietMember, 1)

	batch, err := s.PendingClassification(ctx, 3)
	if err != nil {
		t.Fatalf("очередь: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("в батче %d записей, ожидались три", len(batch))
	}

	var fromQuiet int
	for _, tx := range batch {
		if tx.GroupID == quietGroup.ID {
			fromQuiet++
		}
	}
	if fromQuiet != 1 {
		t.Errorf("записей тихой группы в батче %d, ожидалась одна — обход идёт по кругу", fromQuiet)
	}
	// Первой всё равно берётся самая старая: очередь разгребается от старого
	// к новому, просто не за счёт остальных.
	if batch[0].GroupID != busy.GroupID() {
		t.Errorf("первой взята группа %d, ожидалась самая старая запись шумной (%d)",
			batch[0].GroupID, busy.GroupID())
	}

	// Плательщик приезжает с именем: промпту нужно понять «себе».
	if batch[0].PayerName == "" || batch[0].PayerUserID == 0 {
		t.Errorf("плательщик = %q (%d), очередь обязана отдавать имя и telegram id",
			batch[0].PayerName, batch[0].PayerUserID)
	}
}
