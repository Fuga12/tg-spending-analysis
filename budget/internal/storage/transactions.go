package storage

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// Transaction — строка таблицы transactions.
//
// payer_id — кто заплатил, берётся из telebot и у пользователя не спрашивается.
// beneficiary — на кого потрачено относительно плательщика (§3).
type Transaction struct {
	ID                  int64
	PayerID             int64
	Beneficiary         string
	Kind                string
	Amount              decimal.Decimal
	Description         string
	CategoryID          *int32
	CategoryName        string // заполняется при чтении, join с categories
	RawText             string
	NeedsClassification bool
	SpentAt             time.Time
	CreatedAt           time.Time
}

// InsertTransaction записывает трату и возвращает её id.
func (s *Store) InsertTransaction(ctx context.Context, t Transaction) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		insert into transactions
			(payer_id, beneficiary, kind, amount, description, category_id, raw_text,
			 needs_classification, spent_at)
		values ($1, $2, $3, $4::numeric, $5, $6, $7, $8, $9)
		returning id`,
		t.PayerID, t.Beneficiary, t.Kind, t.Amount.String(), t.Description,
		t.CategoryID, t.RawText, t.NeedsClassification, t.SpentAt).Scan(&id)
	return id, err
}

// Transaction читает одну живую транзакцию.
func (s *Store) Transaction(ctx context.Context, id int64) (Transaction, error) {
	var (
		t      Transaction
		amount string
		cat    *string
	)
	err := s.pool.QueryRow(ctx, `
		select t.id, t.payer_id, t.beneficiary, t.kind, t.amount::text, t.description,
		       t.category_id, c.name, t.raw_text, t.needs_classification, t.spent_at, t.created_at
		from transactions t
		left join categories c on c.id = t.category_id
		where t.id = $1 and t.deleted_at is null`, id).
		Scan(&t.ID, &t.PayerID, &t.Beneficiary, &t.Kind, &amount, &t.Description,
			&t.CategoryID, &cat, &t.RawText, &t.NeedsClassification, &t.SpentAt, &t.CreatedAt)
	if err != nil {
		return Transaction{}, err
	}
	if t.Amount, err = decimal.NewFromString(amount); err != nil {
		return Transaction{}, err
	}
	if cat != nil {
		t.CategoryName = *cat
	}
	return t, nil
}

// SetBeneficiary меняет, на кого потрачено. Правку разрешаем только тому, кто
// платил: чужие траты не свои (§9).
func (s *Store) SetBeneficiary(ctx context.Context, id, payerID int64, beneficiary string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update transactions set beneficiary = $3
		where id = $1 and payer_id = $2 and deleted_at is null`, id, payerID, beneficiary)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetCategory меняет категорию и снимает флаг «разобрать позже».
func (s *Store) SetCategory(ctx context.Context, id, payerID int64, categoryID int32) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update transactions set category_id = $3, needs_classification = false
		where id = $1 and payer_id = $2 and deleted_at is null`, id, payerID, categoryID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteTransaction — удаление только мягкое (§3).
func (s *Store) DeleteTransaction(ctx context.Context, id, payerID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update transactions set deleted_at = now()
		where id = $1 and payer_id = $2 and deleted_at is null`, id, payerID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ExpenseRow — строка для отчёта: ровно то, что нужно четырём блокам §10.
type ExpenseRow struct {
	PayerID      int64
	Beneficiary  string
	Amount       decimal.Decimal
	CategoryName string // пусто — «Без категории»
}

// Expenses отдаёт расходы за период по дате траты. Переводы не расход и в
// отчёты не попадают ни в каком виде (§3).
func (s *Store) Expenses(ctx context.Context, from, to time.Time) ([]ExpenseRow, error) {
	rows, err := s.pool.Query(ctx, `
		select t.payer_id, t.beneficiary, t.amount::text, coalesce(c.name, '')
		from transactions t
		left join categories c on c.id = t.category_id
		where t.deleted_at is null
		  and t.kind = 'expense'
		  and t.spent_at >= $1 and t.spent_at < $2`, from, to)
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
		if err := rows.Scan(&r.PayerID, &r.Beneficiary, &amount, &r.CategoryName); err != nil {
			return nil, err
		}
		if r.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TransactionsBetween отдаёт живые операции за период — для /день.
func (s *Store) TransactionsBetween(ctx context.Context, from, to time.Time) ([]Transaction, error) {
	rows, err := s.pool.Query(ctx, `
		select t.id, t.payer_id, t.beneficiary, t.kind, t.amount::text, t.description,
		       t.category_id, coalesce(c.name, ''), t.raw_text, t.needs_classification,
		       t.spent_at, t.created_at
		from transactions t
		left join categories c on c.id = t.category_id
		where t.deleted_at is null and t.spent_at >= $1 and t.spent_at < $2
		order by t.spent_at, t.id`, from, to)
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
		if err := rows.Scan(&t.ID, &t.PayerID, &t.Beneficiary, &t.Kind, &amount, &t.Description,
			&t.CategoryID, &t.CategoryName, &t.RawText, &t.NeedsClassification,
			&t.SpentAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		if t.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PendingClassification отдаёт записи, которым воркер должен добрать
// категорию (§12).
func (s *Store) PendingClassification(ctx context.Context, limit int) ([]Transaction, error) {
	rows, err := s.pool.Query(ctx, `
		select id, payer_id, beneficiary, kind, amount::text, description, raw_text,
		       spent_at, created_at
		from transactions
		where needs_classification and deleted_at is null
		order by id
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
		if err := rows.Scan(&t.ID, &t.PayerID, &t.Beneficiary, &t.Kind, &amount,
			&t.Description, &t.RawText, &t.SpentAt, &t.CreatedAt); err != nil {
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
// апдейтом: категория, бенефициар, вид операции и дата траты. Отдельные
// апдейты оставляли бы запись в полуразобранном виде (§12).
func (s *Store) ApplyClassification(ctx context.Context, id, payerID int64,
	categoryID *int32, beneficiary, kind string, spentAt time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update transactions
		set category_id = $3, beneficiary = $4, kind = $5, spent_at = $6,
		    needs_classification = false
		where id = $1 and payer_id = $2 and deleted_at is null`,
		id, payerID, categoryID, beneficiary, kind, spentAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// HasRecordedOn — писал ли пользователь боту в этот день. Считается по
// created_at: человек, записавший вечером вчерашние траты, ботом пользовался,
// и дёргать его напоминанием незачем (§12).
func (s *Store) HasRecordedOn(ctx context.Context, userID int64, from, to time.Time) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		select exists (
			select 1 from transactions
			where payer_id = $1 and deleted_at is null
			  and created_at >= $2 and created_at < $3
		)`, userID, from, to).Scan(&exists)
	return exists, err
}
