package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Transaction — строка таблицы transactions.
//
// PayerMemberID — кто заплатил, берётся из telebot и у пользователя не
// спрашивается. Recipients — на кого потрачено; пустой список означает «на всю
// группу», и это не то же самое, что перечислить всех поимённо: состав группы
// меняется, а смысл «общая трата» — нет.
type Transaction struct {
	ID            int64
	GroupID       int64
	PayerMemberID int64
	// PayerUserID заполняется там, где нужен личный словарь плательщика:
	// словарь принадлежит человеку, а не его строке в members.
	PayerUserID int64
	Recipients  []int64

	Kind         string
	Amount       decimal.Decimal
	Description  string
	CategoryID   *int32
	CategoryName string // заполняется при чтении, join с categories
	RawText      string

	NeedsClassification bool
	NeedsReview         bool       // категорию поставил воркер вслепую
	UpdatedAt           *time.Time // версия записи: бот, воркер и приложение правят одну строку
	SpentAt             time.Time
	CreatedAt           time.Time
}

// ErrNoRows — записи нет или она удалена.
var ErrNoRows = errors.New("записи нет")

// InsertTransaction записывает трату вместе с получателями и возвращает её id.
//
// Одной транзакцией: трата без получателей читается как «на всю группу», и
// оборванная на полпути запись молча меняет смысл строки в отчёте.
func (g *GroupStore) InsertTransaction(ctx context.Context, t Transaction) (int64, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx, `
		insert into transactions
			(group_id, payer_member_id, kind, amount, description, category_id,
			 raw_text, needs_classification, spent_at)
		values ($1, $2, $3, $4::numeric, $5, $6, $7, $8, $9)
		returning id`,
		g.groupID, t.PayerMemberID, t.Kind, t.Amount.String(), t.Description,
		t.CategoryID, t.RawText, t.NeedsClassification, t.SpentAt).Scan(&id)
	if err != nil {
		return 0, err
	}

	if err := setRecipients(ctx, tx, g.groupID, id, t.Recipients); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

// setRecipients переписывает список получателей траты.
//
// Участники сверяются с группой прямо в запросе: чужой member_id не пройдёт
// фильтр и просто не запишется. Внешним ключом это не выражается — в
// tx_recipients нет своей колонки группы, и заводить её ради проверки значит
// дублировать группу траты в каждой строке связки.
func setRecipients(ctx context.Context, tx pgx.Tx, groupID, txID int64, members []int64) error {
	if _, err := tx.Exec(ctx,
		`delete from tx_recipients where transaction_id = $1`, txID); err != nil {
		return err
	}
	if len(members) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		insert into tx_recipients (transaction_id, member_id)
		select $1, m.id from members m
		where m.id = any($2) and m.group_id = $3
		on conflict do nothing`, txID, members, groupID)
	return err
}

// SetRecipients меняет, на кого потрачено.
//
// Правит любой участник: бюджет общий, и запрет трогать чужую запись означал
// бы, что опечатку в ней некому исправить, пока автор не дойдёт до телефона.
func (g *GroupStore) SetRecipients(ctx context.Context, id int64, members []int64) (bool, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		update transactions set updated_at = now()
		where id = $1 and group_id = $2 and deleted_at is null`, id, g.groupID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := setRecipients(ctx, tx, g.groupID, id, members); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// Transaction читает одну живую транзакцию.
func (g *GroupStore) Transaction(ctx context.Context, id int64) (Transaction, error) {
	rows, err := g.pool.Query(ctx, txSelect+`
		where t.id = $2 and t.group_id = $1 and t.deleted_at is null`, g.groupID, id)
	if err != nil {
		return Transaction{}, err
	}
	out, err := scanTransactions(rows)
	if err != nil {
		return Transaction{}, err
	}
	if len(out) == 0 {
		return Transaction{}, ErrNoRows
	}
	return out[0], nil
}

// MarkForReview закрывает запись категорией и помечает, что выбрал её не
// человек: приложение покажет такие отдельным блоком.
func (g *GroupStore) MarkForReview(ctx context.Context, id int64, categoryID int32) (bool, error) {
	// Условие needs_classification обязательно: пока воркер ходил в модель,
	// человек мог поставить категорию руками, и затирать её нельзя.
	tag, err := g.pool.Exec(ctx, `
		update transactions
		set category_id = $3, needs_classification = false, needs_review = true,
		    updated_at = now()
		where id = $2 and group_id = $1 and deleted_at is null and needs_classification`,
		g.groupID, id, categoryID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteTransaction — удаление только мягкое. Удалить может любой участник,
// вернуть тоже.
func (g *GroupStore) DeleteTransaction(ctx context.Context, id int64) (bool, error) {
	tag, err := g.pool.Exec(ctx, `
		update transactions set deleted_at = now(), updated_at = now()
		where id = $2 and group_id = $1 and deleted_at is null`, g.groupID, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RestoreTransaction снимает мягкое удаление — за кнопкой «вернуть» в ответе
// бота.
func (g *GroupStore) RestoreTransaction(ctx context.Context, id int64) (bool, error) {
	tag, err := g.pool.Exec(ctx, `
		update transactions set deleted_at = null, updated_at = now()
		where id = $2 and group_id = $1 and deleted_at is not null`, g.groupID, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ExpenseRow — строка для отчёта: плательщик, получатели, сумма, категория.
type ExpenseRow struct {
	PayerMemberID int64
	Recipients    []int64
	Amount        decimal.Decimal
	CategoryID    *int32
	CategoryName  string // пусто — «Без категории»
}

// Expenses отдаёт расходы группы за период по дате траты. Переводы не расход
// и в отчёты не попадают ни в каком виде.
func (g *GroupStore) Expenses(ctx context.Context, from, to time.Time) ([]ExpenseRow, error) {
	rows, err := g.pool.Query(ctx, `
		select t.payer_member_id, `+recipientsExpr+`, t.amount::text,
		       t.category_id, coalesce(c.name, '')
		from transactions t
		left join categories c on c.id = t.category_id
		where t.group_id = $1
		  and t.deleted_at is null
		  and t.kind = 'expense'
		  and t.spent_at >= $2 and t.spent_at < $3`, g.groupID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ExpenseRow
	for rows.Next() {
		var (
			r      ExpenseRow
			amount string
		)
		if err := rows.Scan(&r.PayerMemberID, &r.Recipients, &amount,
			&r.CategoryID, &r.CategoryName); err != nil {
			return nil, err
		}
		if r.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// recipientsExpr собирает получателей траты подзапросом. Отдельным запросом
// по списку id было бы два round-trip там, где хватает одного.
const recipientsExpr = `coalesce((
	select array_agg(r.member_id order by r.member_id)
	from tx_recipients r where r.transaction_id = t.id), '{}')`

// txSelect — общий список колонок для чтения транзакций. Первый параметр
// запроса всегда group_id.
const txSelect = `
	select t.id, t.group_id, t.payer_member_id, ` + recipientsExpr + `,
	       t.kind, t.amount::text, t.description, t.category_id, coalesce(c.name, ''),
	       t.raw_text, t.needs_classification, t.needs_review,
	       t.spent_at, t.created_at, t.updated_at
	from transactions t
	left join categories c on c.id = t.category_id`

func scanTransactions(rows pgx.Rows) ([]Transaction, error) {
	defer rows.Close()

	var out []Transaction
	for rows.Next() {
		var (
			t      Transaction
			amount string
		)
		if err := rows.Scan(&t.ID, &t.GroupID, &t.PayerMemberID, &t.Recipients,
			&t.Kind, &amount, &t.Description, &t.CategoryID, &t.CategoryName,
			&t.RawText, &t.NeedsClassification, &t.NeedsReview,
			&t.SpentAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		var err error
		if t.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TransactionsBetween отдаёт живые операции группы за период.
func (g *GroupStore) TransactionsBetween(ctx context.Context, from, to time.Time) ([]Transaction, error) {
	rows, err := g.pool.Query(ctx, txSelect+`
		where t.group_id = $1 and t.deleted_at is null
		  and t.spent_at >= $2 and t.spent_at < $3
		order by t.spent_at, t.id`, g.groupID, from, to)
	if err != nil {
		return nil, err
	}
	return scanTransactions(rows)
}

// PendingClassification отдаёт записи, которым воркер должен добрать
// категорию. Метод административный: очередь общая на все группы, и воркер
// обходит её целиком.
//
// Порядок — по id, то есть в порядке поступления. Это несправедливо к малым
// группам: одна активная забьёт батч целиком. Справедливый обход — фаза 2.5.
func (s *Store) PendingClassification(ctx context.Context, limit int) ([]Transaction, error) {
	rows, err := s.pool.Query(ctx, `
		select t.id, t.group_id, t.payer_member_id, m.user_id, t.kind, t.amount::text,
		       t.description, t.raw_text, t.spent_at, t.created_at
		from transactions t
		join members m on m.id = t.payer_member_id
		where t.needs_classification and t.deleted_at is null
		order by t.id
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Transaction
	for rows.Next() {
		var (
			t      Transaction
			amount string
		)
		if err := rows.Scan(&t.ID, &t.GroupID, &t.PayerMemberID, &t.PayerUserID,
			&t.Kind, &amount, &t.Description, &t.RawText, &t.SpentAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		if t.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		t.NeedsClassification = true
		out = append(out, t)
	}
	return out, rows.Err()
}

// ApplyClassification доводит деградированную запись до разобранной одним
// апдейтом: категория, вид операции и дата траты. Отдельные апдейты оставляли
// бы запись в полуразобранном виде.
//
// Получателей метод не трогает: их проставил тот, кто записывал трату, и
// модель их пока не определяет. Исключение — перевод: он не трата, и
// получатель у него не имеет смысла.
func (g *GroupStore) ApplyClassification(ctx context.Context, id int64,
	categoryID *int32, kind string, spentAt time.Time) (bool, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		update transactions
		set category_id = $3, kind = $4, spent_at = $5,
		    needs_classification = false, updated_at = now()
		where id = $2 and group_id = $1 and deleted_at is null
		  and needs_classification`,
		g.groupID, id, categoryID, kind, spentAt)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if kind != KindExpense {
		if _, err := tx.Exec(ctx,
			`delete from tx_recipients where transaction_id = $1`, id); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// Виды операций. Значения те же, что в classify.Kind*, но зависеть от
// classify отсюда нельзя: она сама зависит от storage.
const (
	KindExpense  = "expense"
	KindIncome   = "income"
	KindTransfer = "transfer"
)

// HasRecordedOn — писал ли человек боту в этот день. Считается по created_at:
// человек, записавший вечером вчерашние траты, ботом пользовался, и дёргать
// его напоминанием незачем.
//
// Метод спрашивает про человека, а не про группу: состоит он ровно в одной,
// и напоминание приходит ему, а не группе.
func (s *Store) HasRecordedOn(ctx context.Context, userID int64, from, to time.Time) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		select exists (
			select 1 from transactions t
			join members m on m.id = t.payer_member_id
			where m.user_id = $1 and t.deleted_at is null
			  and t.created_at >= $2 and t.created_at < $3
		)`, userID, from, to).Scan(&exists)
	return exists, err
}
