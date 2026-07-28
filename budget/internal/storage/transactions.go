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
