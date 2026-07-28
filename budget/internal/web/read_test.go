package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

// loggedIn поднимает сервер и входит, возвращая готовый клиент.
func loggedIn(t *testing.T) (*storage.Store, string, *http.Client) {
	t.Helper()
	store, base, client := authServer(t)

	resp, err := client.Get(base + "/auth?token=" + issueToken(t, store, testUserID))
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	resp.Body.Close()

	if _, err := store.Pool().Exec(context.Background(), `truncate transactions restart identity`); err != nil {
		t.Fatalf("очистка: %v", err)
	}
	return store, base, client
}

func addTx(t *testing.T, store *storage.Store, payer int64, amount, desc, kind string, spentAt time.Time, cat *int32) int64 {
	t.Helper()
	id, err := store.InsertTransaction(context.Background(), storage.Transaction{
		PayerID: payer, Beneficiary: classify.BenBoth, Kind: kind,
		Amount: decimal.RequireFromString(amount), Description: desc,
		CategoryID: cat, RawText: desc + " " + amount, SpentAt: spentAt,
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	return id
}

func getJSON(t *testing.T, client *http.Client, url string, out any) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("запрос %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: код %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("разбор %s: %v", url, err)
	}
}

func TestTransactionsList(t *testing.T) {
	store, base, client := loggedIn(t)
	now := time.Now()

	cats, _ := store.Categories(context.Background())
	var food int32
	for _, c := range cats {
		if c.Name == "Продукты" {
			food = c.ID
		}
	}

	addTx(t, store, testUserID, "1200", "пятёрочка", classify.KindExpense, now, &food)
	addTx(t, store, testUserID, "400", "такси", classify.KindExpense, now, nil)
	addTx(t, store, testUserID, "5000", "перевод", classify.KindTransfer, now, nil)
	addTx(t, store, testUserID, "700", "прошлый месяц", classify.KindExpense, now.AddDate(0, 0, -45), &food)

	var list listResponse
	getJSON(t, client, base+"/api/transactions", &list)

	if list.Total != 3 {
		t.Fatalf("операций %d, ожидались три за текущий месяц (%+v)", list.Total, list.Items)
	}
	for _, item := range list.Items {
		if item.Day == "" {
			t.Error("день должен считать сервер, а не браузер")
		}
		if !item.Mine {
			t.Errorf("запись %d должна быть своей", item.ID)
		}
	}

	// Фильтр по категории.
	var filtered listResponse
	getJSON(t, client, base+"/api/transactions?category="+itoa(int(food)), &filtered)
	if filtered.Total != 1 || filtered.Items[0].Description != "пятёрочка" {
		t.Errorf("фильтр по категории дал %+v", filtered.Items)
	}
}

func TestSearchAcrossMonths(t *testing.T) {
	store, base, client := loggedIn(t)
	now := time.Now()

	addTx(t, store, testUserID, "1200", "пятёрочка", classify.KindExpense, now.AddDate(0, 0, -70), nil)
	addTx(t, store, testUserID, "450", "такси", classify.KindExpense, now, nil)

	// По описанию — и находит запись двухмесячной давности (§3.2).
	var byText listResponse
	getJSON(t, client, base+"/api/transactions?q=пятёр", &byText)
	if byText.Total != 1 || byText.Items[0].Description != "пятёрочка" {
		t.Errorf("поиск по описанию дал %+v", byText.Items)
	}

	// И по сумме.
	var byAmount listResponse
	getJSON(t, client, base+"/api/transactions?q=450", &byAmount)
	if byAmount.Total != 1 || byAmount.Items[0].Description != "такси" {
		t.Errorf("поиск по сумме дал %+v", byAmount.Items)
	}
}

func TestMonthReportMatchesBot(t *testing.T) {
	store, base, client := loggedIn(t)
	now := time.Now()

	cats, _ := store.Categories(context.Background())
	var food int32
	for _, c := range cats {
		if c.Name == "Продукты" {
			food = c.ID
		}
	}

	addTx(t, store, testUserID, "1200", "пятёрочка", classify.KindExpense, now, &food)
	addTx(t, store, testUserID, "800", "кофе", classify.KindExpense, now, &food)
	// Перевод и доход в отчёт не входят — как и в боте.
	addTx(t, store, testUserID, "5000", "перевод", classify.KindTransfer, now, nil)
	addTx(t, store, testUserID, "90000", "зарплата", classify.KindIncome, now, nil)

	var m monthResponse
	getJSON(t, client, base+"/api/report/month", &m)

	if m.Total != "2000" {
		t.Errorf("итог = %s, ожидалось 2000: переводы и доходы не расход", m.Total)
	}
	if len(m.Categories) != 1 || m.Categories[0].Name != "Продукты" {
		t.Errorf("категории = %+v", m.Categories)
	}
	if len(m.Payers) != 1 || m.Payers[0].ID != testUserID {
		t.Errorf("кто платил = %+v, у строки должен быть id для цветового слота", m.Payers)
	}
}

func TestPendingCounted(t *testing.T) {
	store, base, client := loggedIn(t)
	now := time.Now()

	cats, _ := store.Categories(context.Background())
	var other int32
	for _, c := range cats {
		if c.Name == classify.CategoryOther {
			other = c.ID
		}
	}

	id := addTx(t, store, testUserID, "600", "непонятное", classify.KindExpense, now, nil)
	if _, err := store.MarkForReview(context.Background(), id, other); err != nil {
		t.Fatalf("пометка: %v", err)
	}
	addTx(t, store, testUserID, "400", "такси", classify.KindExpense, now, &other)

	var m monthResponse
	getJSON(t, client, base+"/api/report/month", &m)
	if m.Pending != 1 {
		t.Errorf("на проверку %d, ожидалась одна: вторая запись с «Прочим» выбрана честно", m.Pending)
	}

	var list listResponse
	getJSON(t, client, base+"/api/transactions?pending=1", &list)
	if list.Total != 1 || list.Items[0].ID != id {
		t.Errorf("фильтр «на проверку» дал %+v", list.Items)
	}
}

func TestPagination(t *testing.T) {
	store, base, client := loggedIn(t)
	now := time.Now()

	for i := 0; i < 5; i++ {
		addTx(t, store, testUserID, "100", "трата", classify.KindExpense, now, nil)
	}

	var page listResponse
	getJSON(t, client, base+"/api/transactions?limit=2", &page)
	if len(page.Items) != 2 || page.Total != 5 || !page.HasMore {
		t.Errorf("страница = %d из %d, ещё есть: %v", len(page.Items), page.Total, page.HasMore)
	}

	var last listResponse
	getJSON(t, client, base+"/api/transactions?limit=2&offset=4", &last)
	if len(last.Items) != 1 || last.HasMore {
		t.Errorf("последняя страница = %d, ещё есть: %v", len(last.Items), last.HasMore)
	}
}

func TestBadMonthRejected(t *testing.T) {
	_, base, client := loggedIn(t)

	resp, err := client.Get(base + "/api/report/month?month=13")
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("код = %d, ожидался 400", resp.StatusCode)
	}
}

func itoa(n int) string {
	return json.Number(decimal.NewFromInt(int64(n)).String()).String()
}
