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
// резать по UTC, вечерние траты уезжают на день вперёд (webapp-design.md §3.9).
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
