package storage

import (
	"context"
	"fmt"
)

// BeneficiaryGroup — произвольная группа получателей: например, «Другие»
// или «Подарки». В транзакции хранится стабильный ключ group:<id>.
type BeneficiaryGroup struct {
	ID   int32
	Name string
}

func (g BeneficiaryGroup) Key() string { return fmt.Sprintf("group:%d", g.ID) }

func (s *Store) BeneficiaryGroups(ctx context.Context) ([]BeneficiaryGroup, error) {
	rows, err := s.pool.Query(ctx, `
		select id, name from beneficiary_groups order by sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BeneficiaryGroup
	for rows.Next() {
		var g BeneficiaryGroup
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) CreateBeneficiaryGroup(ctx context.Context, name string) (BeneficiaryGroup, error) {
	var g BeneficiaryGroup
	err := s.pool.QueryRow(ctx, `
		insert into beneficiary_groups (name, sort_order)
		values ($1, (select coalesce(max(sort_order), 0) + 10 from beneficiary_groups))
		returning id, name`, name).Scan(&g.ID, &g.Name)
	return g, err
}

func (s *Store) UpdateBeneficiaryGroup(ctx context.Context, id int32, name string) (BeneficiaryGroup, error) {
	var g BeneficiaryGroup
	err := s.pool.QueryRow(ctx, `
		update beneficiary_groups set name = $2 where id = $1
		returning id, name`, id, name).Scan(&g.ID, &g.Name)
	return g, err
}

// DeleteBeneficiaryGroup удаляет только пустую группу. Исторические траты и
// умолчания категорий нельзя молча переносить в «Общее».
func (s *Store) DeleteBeneficiaryGroup(ctx context.Context, id int32) (bool, bool, error) {
	key := fmt.Sprintf("group:%d", id)
	var used bool
	err := s.pool.QueryRow(ctx, `
		select exists(select 1 from transactions where beneficiary = $1)
		    or exists(select 1 from categories where default_beneficiary = $1)
		    or exists(select 1 from word_map where beneficiary = $1)`, key).Scan(&used)
	if err != nil || used {
		return false, used, err
	}
	tag, err := s.pool.Exec(ctx, `delete from beneficiary_groups where id = $1`, id)
	if err != nil {
		return false, false, err
	}
	return tag.RowsAffected() > 0, false, nil
}
