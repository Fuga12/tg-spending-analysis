package storage

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// DayTotal — расход за один день месяца.
type DayTotal struct {
	Day    string
	Amount decimal.Decimal
}

// DailyExpenses считает расходы по дням. Группировка в таймзоне бота: если
// резать по UTC, вечерние траты уезжают на день вперёд.
func (g *GroupStore) DailyExpenses(ctx context.Context, from, to time.Time, tz string) ([]DayTotal, error) {
	rows, err := g.pool.Query(ctx, `
		select to_char((spent_at at time zone $4)::date, 'YYYY-MM-DD') as day,
		       sum(amount)::text
		from transactions
		where group_id = $1 and deleted_at is null and kind = 'expense'
		  and spent_at >= $2 and spent_at < $3
		group by day
		order by day`, g.groupID, from, to, tz)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DayTotal
	for rows.Next() {
		var (
			d      DayTotal
			amount string
		)
		if err := rows.Scan(&d.Day, &amount); err != nil {
			return nil, err
		}
		if d.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MonthTotal — итог одного месяца.
type MonthTotal struct {
	Year   int
	Month  int
	Amount decimal.Decimal
}

// MonthlyExpenses отдаёт итоги месяцев в порядке возрастания. Границы
// считает вызывающий: правило «какие месяцы показывать» — его дело.
func (g *GroupStore) MonthlyExpenses(ctx context.Context, from, to time.Time, tz string) ([]MonthTotal, error) {
	rows, err := g.pool.Query(ctx, `
		select extract(year from (spent_at at time zone $4))::int,
		       extract(month from (spent_at at time zone $4))::int,
		       sum(amount)::text
		from transactions
		where group_id = $1 and deleted_at is null and kind = 'expense'
		  and spent_at >= $2 and spent_at < $3
		group by 1, 2
		order by 1, 2`, g.groupID, from, to, tz)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MonthTotal
	for rows.Next() {
		var (
			m      MonthTotal
			amount string
		)
		if err := rows.Scan(&m.Year, &m.Month, &amount); err != nil {
			return nil, err
		}
		if m.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// TotalExpenses — сумма расходов за отрезок. Нужна сравнению месяцев:
// раскладывать по категориям ради одного числа незачем.
func (g *GroupStore) TotalExpenses(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	var amount string
	err := g.pool.QueryRow(ctx, `
		select coalesce(sum(amount), 0)::text from transactions
		where group_id = $1 and deleted_at is null and kind = 'expense'
		  and spent_at >= $2 and spent_at < $3`, g.groupID, from, to).Scan(&amount)
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(amount)
}

// DayCategory — сколько ушло в категорию за один день.
type DayCategory struct {
	Day        string
	CategoryID *int32
	Amount     decimal.Decimal
}

// DailyByCategory раскладывает расходы по дням и категориям одним запросом.
//
// Отдельными запросами на каждый день это было бы тридцать round-trip ради
// одного графика. Группировка в таймзоне бота: если резать по UTC, вечерние
// траты уезжают на день вперёд.
func (g *GroupStore) DailyByCategory(ctx context.Context, from, to time.Time, tz string) ([]DayCategory, error) {
	rows, err := g.pool.Query(ctx, `
		select to_char((spent_at at time zone $4)::date, 'YYYY-MM-DD') as day,
		       category_id, sum(amount)::text
		from transactions
		where group_id = $1 and deleted_at is null and kind = 'expense'
		  and spent_at >= $2 and spent_at < $3
		group by day, category_id
		order by day`, g.groupID, from, to, tz)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DayCategory
	for rows.Next() {
		var (
			d      DayCategory
			amount string
		)
		if err := rows.Scan(&d.Day, &d.CategoryID, &amount); err != nil {
			return nil, err
		}
		if d.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
