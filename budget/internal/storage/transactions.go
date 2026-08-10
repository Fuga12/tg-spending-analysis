package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
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
	NeedsReview         bool       // категорию поставил воркер вслепую
	UpdatedAt           *time.Time // версия записи: сайт, бот и воркер правят одну строку
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
		       t.category_id, c.name, t.raw_text, t.needs_classification, t.needs_review,
		       t.spent_at, t.created_at, t.updated_at
		from transactions t
		left join categories c on c.id = t.category_id
		where t.id = $1 and t.deleted_at is null`, id).
		Scan(&t.ID, &t.PayerID, &t.Beneficiary, &t.Kind, &amount, &t.Description,
			&t.CategoryID, &cat, &t.RawText, &t.NeedsClassification, &t.NeedsReview,
			&t.SpentAt, &t.CreatedAt, &t.UpdatedAt)
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

// SetBeneficiary меняет, на кого потрачено.
//
// Правит любой из двоих: бюджет общий, и запрет трогать запись партнёра
// означал бы, что опечатку в его трате некому исправить, пока он не дойдёт
// до телефона. Это осознанное отступление от plan.md §9.
func (s *Store) SetBeneficiary(ctx context.Context, id int64, beneficiary string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update transactions set beneficiary = $2, updated_at = now()
		where id = $1 and deleted_at is null`, id, beneficiary)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetCategory меняет категорию и снимает флаг «разобрать позже».
func (s *Store) SetCategory(ctx context.Context, id int64, categoryID int32) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update transactions
		set category_id = $2, needs_classification = false, needs_review = false,
		    updated_at = now()
		where id = $1 and deleted_at is null`, id, categoryID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MarkForReview закрывает запись категорией и помечает, что выбрал её не
// человек: сайт покажет такие отдельным блоком (webapp-design.md §3.8).
func (s *Store) MarkForReview(ctx context.Context, id int64, categoryID int32) (bool, error) {
	// Условие needs_classification обязательно: пока воркер ходил в модель,
	// человек мог поставить категорию руками, и затирать её нельзя.
	tag, err := s.pool.Exec(ctx, `
		update transactions
		set category_id = $2, needs_classification = false, needs_review = true,
		    updated_at = now()
		where id = $1 and deleted_at is null and needs_classification`, id, categoryID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteTransaction — удаление только мягкое (§3). Удалить может любой
// из двоих, вернуть тоже.
func (s *Store) DeleteTransaction(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update transactions set deleted_at = now()
		where id = $1 and deleted_at is null`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ExpenseRow — строка для отчёта: ровно то, что нужно четырём блокам §10.
type ExpenseRow struct {
	PayerID         int64
	Beneficiary     string
	BeneficiaryName string // имя произвольной группы, если это group:<id>
	Amount          decimal.Decimal
	CategoryID      *int32
	CategoryName    string // пусто — «Без категории»
}

// Expenses отдаёт расходы за период по дате траты. Переводы не расход и в
// отчёты не попадают ни в каком виде (§3).
func (s *Store) Expenses(ctx context.Context, from, to time.Time) ([]ExpenseRow, error) {
	rows, err := s.pool.Query(ctx, `
		select t.payer_id, t.beneficiary, coalesce(bg.name, ''),
		       t.amount::text, t.category_id, coalesce(c.name, '')
		from transactions t
		left join categories c on c.id = t.category_id
		left join beneficiary_groups bg on t.beneficiary = 'group:' || bg.id::text
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
		if err := rows.Scan(&r.PayerID, &r.Beneficiary, &r.BeneficiaryName,
			&amount, &r.CategoryID, &r.CategoryName); err != nil {
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
		    needs_classification = false, updated_at = now()
		where id = $1 and payer_id = $2 and deleted_at is null
		  and needs_classification`,
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

// ErrNoRows — записи нет или она удалена. ErrNotOwner — запись чужая:
// различать важно, иначе человека обвиняют в чужой трате на его же (§9).
var (
	ErrNoRows   = errors.New("записи нет")
	ErrNotOwner = errors.New("запись чужая")
)

// TransactionFilter — что показать в списке. Пустые поля означают «всё».
type TransactionFilter struct {
	From, To  time.Time
	PayerID   int64
	Category  int32
	Recipient string // both, group:<id> или user:<id>
	Kind      string
	Pending   bool   // только записи, разобранные вслепую
	Query     string // поиск по описанию и сумме
	Limit     int
	Offset    int
}

// ListTransactions отдаёт страницу операций и общее число подходящих.
// Общее число нужно кнопке «показать ещё»: без него неясно, есть ли хвост.
func (s *Store) ListTransactions(ctx context.Context, f TransactionFilter) ([]Transaction, int, error) {
	where := []string{"t.deleted_at is null"}
	args := []any{}
	add := func(cond string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}

	if !f.From.IsZero() {
		add("t.spent_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("t.spent_at < $%d", f.To)
	}
	if f.PayerID != 0 {
		add("t.payer_id = $%d", f.PayerID)
	}
	if f.Category != 0 {
		add("t.category_id = $%d", f.Category)
	}
	if f.Recipient == "both" || strings.HasPrefix(f.Recipient, "group:") {
		add("t.beneficiary = $%d", f.Recipient)
	} else if strings.HasPrefix(f.Recipient, "user:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(f.Recipient, "user:"), 10, 64)
		if err == nil && id > 0 {
			args = append(args, id)
			n := len(args)
			where = append(where, fmt.Sprintf(
				"((t.beneficiary = 'payer' and t.payer_id = $%d) or (t.beneficiary = 'partner' and t.payer_id <> $%d))",
				n, n))
		}
	}
	if f.Kind != "" {
		add("t.kind = $%d", f.Kind)
	}
	if f.Pending {
		// Только needs_review: needs_classification живёт минуты и означает
		// «воркер ещё не дошёл», а не «нужен человек» (webapp-design.md §3.8).
		where = append(where, "t.needs_review")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		// Ищем и по описанию, и по сумме: «1200» должно находиться так же,
		// как «пятёрочка».
		args = append(args, "%"+escapeLike(q)+"%")
		byText := fmt.Sprintf("t.description ilike $%d", len(args))
		// Числом считаем только то, что похоже на сумму: «1e2147483000»
		// разворачивается decimal в гигабайты и кладёт процесс вместе с ботом.
		if amountLike.MatchString(q) {
			amount := decimal.RequireFromString(strings.ReplaceAll(q, ",", "."))
			args = append(args, amount.String())
			byText += fmt.Sprintf(" or t.amount = $%d::numeric", len(args))
		}
		where = append(where, "("+byText+")")
	}

	cond := strings.Join(where, " and ")

	var total int
	if err := s.pool.QueryRow(ctx,
		`select count(*) from transactions t where `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args = append(args, limit, f.Offset)

	rows, err := s.pool.Query(ctx, `
		select t.id, t.payer_id, t.beneficiary, t.kind, t.amount::text, t.description,
		       t.category_id, coalesce(c.name, ''), t.raw_text,
		       t.needs_classification, t.needs_review, t.spent_at, t.created_at, t.updated_at
		from transactions t
		left join categories c on c.id = t.category_id
		where `+cond+`
		order by t.spent_at desc, t.id desc
		limit $`+strconv.Itoa(len(args)-1)+` offset $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []Transaction
	for rows.Next() {
		var (
			t      Transaction
			amount string
		)
		if err := rows.Scan(&t.ID, &t.PayerID, &t.Beneficiary, &t.Kind, &amount, &t.Description,
			&t.CategoryID, &t.CategoryName, &t.RawText,
			&t.NeedsClassification, &t.NeedsReview, &t.SpentAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if t.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// amountLike — то, что можно считать суммой в поиске.
var amountLike = regexp.MustCompile(`^\d{1,12}([.,]\d{1,2})?$`)

// escapeLike обезвреживает метасимволы LIKE: иначе «%» находит всё.
func escapeLike(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return r.Replace(s)
}

// PendingReview — сколько записей ждут человека (§3.8 webapp-design.md).
func (s *Store) PendingReview(ctx context.Context, from, to time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		select count(*) from transactions
		where deleted_at is null and needs_review
		  and spent_at >= $1 and spent_at < $2`, from, to).Scan(&n)
	return n, err
}

// ErrVersionConflict — запись изменили, пока её правили. Возвращается вместо
// молчаливой перезаписи: одну запись правят сайт, бот и воркер (webapp.md §4).
var ErrVersionConflict = errors.New("запись изменилась")

// TransactionPatch — что меняем. Nil-поля не трогаются.
type TransactionPatch struct {
	Amount      *decimal.Decimal
	Description *string
	CategoryID  **int32 // указатель на указатель: nil — не трогать, *nil — обнулить
	Beneficiary *string
	Kind        *string
	SpentAt     *time.Time
	Restore     bool
}

// UpdateTransaction применяет правку с проверкой версии.
//
// expected — updated_at, который видел клиент (nil, если запись ещё не
// правили). Расхождение означает, что запись успели изменить: возвращаем
// ErrVersionConflict, а не затираем чужую работу.
func (s *Store) UpdateTransaction(ctx context.Context, id int64, p TransactionPatch, expected *time.Time) (Transaction, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transaction{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		current   time.Time
		hasUpd    bool
		updatedAt *time.Time
		deleted   *time.Time
	)
	err = tx.QueryRow(ctx, `
		select updated_at, deleted_at from transactions
		where id = $1 for update`, id).Scan(&updatedAt, &deleted)
	if err != nil {
		if isNoRows(err) {
			return Transaction{}, ErrNoRows
		}
		return Transaction{}, err
	}
	if deleted != nil && !p.Restore {
		return Transaction{}, ErrNoRows
	}
	if updatedAt != nil {
		current, hasUpd = *updatedAt, true
	}
	if !sameVersion(expected, current, hasUpd) {
		return Transaction{}, ErrVersionConflict
	}

	set := []string{"updated_at = now()"}
	args := []any{id}
	add := func(expr string, val any) {
		args = append(args, val)
		set = append(set, fmt.Sprintf(expr, len(args)))
	}

	if p.Amount != nil {
		add("amount = $%d::numeric", p.Amount.String())
	}
	if p.Description != nil {
		add("description = $%d", *p.Description)
	}
	if p.CategoryID != nil {
		add("category_id = $%d", *p.CategoryID)
		if *p.CategoryID != nil {
			// Категорию поставил человек — воркеру тут больше делать нечего.
			// А вот снятие категории с очереди не снимает: наоборот, разобрать
			// такую запись ещё нужно.
			set = append(set, "needs_classification = false", "needs_review = false")
		}
	}
	if p.Beneficiary != nil {
		add("beneficiary = $%d", *p.Beneficiary)
	}
	if p.Kind != nil {
		add("kind = $%d", *p.Kind)
	}
	if p.SpentAt != nil {
		add("spent_at = $%d", *p.SpentAt)
	}
	if p.Restore {
		set = append(set, "deleted_at = null")
	}

	if _, err := tx.Exec(ctx,
		`update transactions set `+strings.Join(set, ", ")+` where id = $1`, args...); err != nil {
		return Transaction{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Transaction{}, err
	}

	updated, err := s.Transaction(ctx, id)
	if err != nil && isNoRows(err) {
		// Запись успели удалить между commit и чтением.
		return Transaction{}, ErrNoRows
	}
	return updated, err
}

// RestoreTransaction снимает мягкое удаление. Версия здесь не проверяется:
// у клиента её нет и быть не может, а без возврата кнопка «Вернуть» в тосте
// была бы обманом (webapp-design.md §4.2).
func (s *Store) RestoreTransaction(ctx context.Context, id int64) (Transaction, error) {
	tag, err := s.pool.Exec(ctx, `
		update transactions set deleted_at = null, updated_at = now()
		where id = $1`, id)
	if err != nil {
		return Transaction{}, err
	}
	if tag.RowsAffected() == 0 {
		return Transaction{}, ErrNoRows
	}
	return s.Transaction(ctx, id)
}

// sameVersion сверяет версию с точностью до микросекунды: postgres хранит
// timestamptz именно так, и round-trip через JSON не должен ломать сравнение.
func sameVersion(expected *time.Time, current time.Time, hasCurrent bool) bool {
	if expected == nil {
		return !hasCurrent
	}
	if !hasCurrent {
		return false
	}
	return expected.UnixMicro() == current.UnixMicro()
}
