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

// TransactionFilter — что показать в списке. Пустые поля означают «всё».
type TransactionFilter struct {
	From, To      time.Time
	PayerMemberID int64
	// RecipientMemberID — кому досталось. Ноль означает «всё»; отрицательное
	// значение — «на всю группу», то есть записи без получателей вовсе.
	RecipientMemberID int64
	Category          int32
	Kind              string
	Pending           bool   // только записи, разобранные вслепую
	Query             string // поиск по описанию и сумме
	Limit             int
	Offset            int
}

// CommonRecipient — значение RecipientMemberID для фильтра «на всю группу».
// Отдельным значением, а не флагом: у фильтра одно поле и одно состояние.
const CommonRecipient = -1

// ListTransactions отдаёт страницу операций группы и общее число подходящих.
// Общее число нужно кнопке «показать ещё»: без него неясно, есть ли хвост.
func (g *GroupStore) ListTransactions(ctx context.Context, f TransactionFilter) ([]Transaction, int, error) {
	where := []string{"t.group_id = $1", "t.deleted_at is null"}
	args := []any{g.groupID}
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
	if f.PayerMemberID != 0 {
		add("t.payer_member_id = $%d", f.PayerMemberID)
	}
	switch {
	case f.RecipientMemberID == CommonRecipient:
		where = append(where,
			"not exists (select 1 from tx_recipients r where r.transaction_id = t.id)")
	case f.RecipientMemberID > 0:
		add("exists (select 1 from tx_recipients r where r.transaction_id = t.id and r.member_id = $%d)",
			f.RecipientMemberID)
	}
	if f.Category != 0 {
		add("t.category_id = $%d", f.Category)
	}
	if f.Kind != "" {
		add("t.kind = $%d", f.Kind)
	}
	if f.Pending {
		// Только needs_review: needs_classification живёт минуты и означает
		// «воркер ещё не дошёл», а не «нужен человек».
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
	if err := g.pool.QueryRow(ctx,
		`select count(*) from transactions t where `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args = append(args, limit, f.Offset)

	rows, err := g.pool.Query(ctx, txSelect+`
		where `+cond+`
		order by t.spent_at desc, t.id desc
		limit $`+strconv.Itoa(len(args)-1)+` offset $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	out, err := scanTransactions(rows)
	return out, total, err
}

// amountLike — то, что можно считать суммой в поиске.
var amountLike = regexp.MustCompile(`^\d{1,12}([.,]\d{1,2})?$`)

// escapeLike обезвреживает метасимволы LIKE: иначе «%» находит всё.
func escapeLike(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return r.Replace(s)
}

// PendingReview — сколько записей за период ждут человека.
func (g *GroupStore) PendingReview(ctx context.Context, from, to time.Time) (int, error) {
	var n int
	err := g.pool.QueryRow(ctx, `
		select count(*) from transactions
		where group_id = $1 and deleted_at is null and needs_review
		  and spent_at >= $2 and spent_at < $3`, g.groupID, from, to).Scan(&n)
	return n, err
}

// ErrVersionConflict — запись изменили, пока её правили. Возвращается вместо
// молчаливой перезаписи: одну строку правят бот, воркер и приложение.
var ErrVersionConflict = errors.New("запись изменилась")

// TransactionPatch — что меняем. Nil-поля не трогаются.
type TransactionPatch struct {
	Amount      *decimal.Decimal
	Description *string
	CategoryID  **int32  // указатель на указатель: nil — не трогать, *nil — обнулить
	Recipients  *[]int64 // пустой непустой указатель означает «на всю группу»
	Kind        *string
	SpentAt     *time.Time
}

// UpdateTransaction применяет правку с проверкой версии.
//
// expected — updated_at, который видел клиент (nil, если запись ещё не
// правили). Расхождение означает, что запись успели изменить: возвращаем
// ErrVersionConflict, а не затираем чужую работу. Одну и ту же трату правят
// с двух телефонов чаще, чем кажется.
func (g *GroupStore) UpdateTransaction(ctx context.Context, id int64, p TransactionPatch, expected *time.Time) (Transaction, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return Transaction{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		updatedAt *time.Time
		deleted   *time.Time
	)
	err = tx.QueryRow(ctx, `
		select updated_at, deleted_at from transactions
		where id = $1 and group_id = $2 for update`, id, g.groupID).Scan(&updatedAt, &deleted)
	if err != nil {
		if isNoRows(err) {
			return Transaction{}, ErrNoRows
		}
		return Transaction{}, err
	}
	if deleted != nil {
		return Transaction{}, ErrNoRows
	}
	if !sameVersion(expected, updatedAt) {
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
	if p.Kind != nil {
		add("kind = $%d", *p.Kind)
	}
	if p.SpentAt != nil {
		add("spent_at = $%d", *p.SpentAt)
	}

	if _, err := tx.Exec(ctx,
		`update transactions set `+strings.Join(set, ", ")+` where id = $1`, args...); err != nil {
		return Transaction{}, err
	}
	if p.Recipients != nil {
		if err := setRecipients(ctx, tx, g.groupID, id, *p.Recipients); err != nil {
			return Transaction{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Transaction{}, err
	}
	return g.Transaction(ctx, id)
}

// sameVersion сверяет версию с точностью до микросекунды: postgres хранит
// timestamptz именно так, и round-trip через JSON не должен ломать сравнение.
func sameVersion(expected, current *time.Time) bool {
	if expected == nil || current == nil {
		return expected == nil && current == nil
	}
	return expected.UnixMicro() == current.UnixMicro()
}
