package web

import (
	"testing"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
)

func TestRowsForRecipientResolvesPersonAndGroup(t *testing.T) {
	users := []storage.User{{ID: 1, Name: "Илья"}, {ID: 2, Name: "Уля"}}
	rows := []storage.ExpenseRow{
		{PayerID: 1, Beneficiary: "payer", Amount: decimal.NewFromInt(100)},
		{PayerID: 2, Beneficiary: "partner", Amount: decimal.NewFromInt(200)},
		{PayerID: 2, Beneficiary: "payer", Amount: decimal.NewFromInt(300)},
		{PayerID: 1, Beneficiary: "both", Amount: decimal.NewFromInt(400)},
		{PayerID: 1, Beneficiary: "group:1", Amount: decimal.NewFromInt(500)},
	}

	for _, tc := range []struct {
		name string
		key  string
		want int64
	}{
		{name: "Илья", key: "user:1", want: 300},
		{name: "Уля", key: "user:2", want: 300},
		{name: "общее", key: "both", want: 400},
		{name: "группа", key: "group:1", want: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got int64
			for _, row := range rowsForRecipient(rows, tc.key, users) {
				got += row.Amount.IntPart()
			}
			if got != tc.want {
				t.Fatalf("сумма = %d, ожидалось %d", got, tc.want)
			}
		})
	}
}
