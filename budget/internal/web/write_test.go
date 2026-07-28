package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/classify"
	"budget/internal/storage"
)

func send(t *testing.T, client *http.Client, method, url string, body any) (*http.Response, []byte) {
	t.Helper()

	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()

	out := new(bytes.Buffer)
	_, _ = out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

func TestEditAmount(t *testing.T) {
	// Ради этого сайт и затевался: в боте сумму не исправить вообще.
	store, base, client := loggedIn(t)
	id := addTx(t, store, testUserID, "12000", "пятёрочка", classify.KindExpense, time.Now(), nil)

	resp, body := send(t, client, http.MethodPatch, base+"/api/transactions/"+itoa(int(id)),
		map[string]any{"amount": "1200,50"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}

	var got txView
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if got.Amount != "1200.5" {
		t.Errorf("сумма = %s, ожидалось 1200.5", got.Amount)
	}
	if got.UpdatedAt == nil {
		t.Error("правка должна проставлять версию записи")
	}
}

func TestEditRejectsNonsense(t *testing.T) {
	store, base, client := loggedIn(t)
	id := addTx(t, store, testUserID, "600", "тест", classify.KindExpense, time.Now(), nil)
	url := base + "/api/transactions/" + itoa(int(id))

	cases := []struct {
		name string
		body map[string]any
	}{
		{"ноль", map[string]any{"amount": "0"}},
		{"минус", map[string]any{"amount": "-100"}},
		{"не число", map[string]any{"amount": "много"}},
		{"астрономическая", map[string]any{"amount": "99999999999999"}},
		{"дата в будущем", map[string]any{"spent_at": "2035-01-01"}},
		{"дата из прошлого века", map[string]any{"spent_at": "1999-01-01"}},
		{"выдуманный тип", map[string]any{"kind": "покупка"}},
		{"выдуманный бенефициар", map[string]any{"beneficiary": "нам обоим"}},
		{"нет такой категории", map[string]any{"category_id": 9999}},
		{"пустое описание", map[string]any{"description": "   "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := send(t, client, http.MethodPatch, url, c.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("код = %d, тело %s", resp.StatusCode, body)
			}
		})
	}
}

func TestEditRequiresVersion(t *testing.T) {
	// Запись изменили, пока её правили: затирать чужую работу нельзя.
	store, base, client := loggedIn(t)
	id := addTx(t, store, testUserID, "600", "тест", classify.KindExpense, time.Now(), nil)
	url := base + "/api/transactions/" + itoa(int(id))

	// Первая правка — версии ещё нет.
	resp, body := send(t, client, http.MethodPatch, url, map[string]any{"description": "первая"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}
	var first txView
	_ = json.Unmarshal(body, &first)

	// Кто-то другой правит ту же запись.
	if _, err := store.SetBeneficiary(context.Background(), id, testUserID, classify.BenBoth); err != nil {
		t.Fatalf("чужая правка: %v", err)
	}

	// Наша вторая правка со старой версией должна отбиться.
	resp, body = send(t, client, http.MethodPatch, url,
		map[string]any{"description": "вторая", "updated_at": *first.UpdatedAt})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("код = %d, ожидался 409; тело %s", resp.StatusCode, body)
	}

	var conflict struct {
		Error   string `json:"error"`
		Current txView `json:"current"`
	}
	if err := json.Unmarshal(body, &conflict); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if conflict.Current.Beneficiary != classify.BenBoth {
		t.Errorf("в ответе должно быть текущее состояние, получено %+v", conflict.Current)
	}
	if conflict.Current.Description != "первая" {
		t.Error("чужая правка не должна была затереться нашей")
	}
}

func TestCannotEditPartnersRecord(t *testing.T) {
	store, base, client := loggedIn(t)
	if err := store.UpsertUser(context.Background(), 1002, "Аня"); err != nil {
		t.Fatalf("партнёр: %v", err)
	}
	id := addTx(t, store, 1002, "900", "её трата", classify.KindExpense, time.Now(), nil)

	resp, _ := send(t, client, http.MethodPatch, base+"/api/transactions/"+itoa(int(id)),
		map[string]any{"amount": "100"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("код = %d, ожидался 403", resp.StatusCode)
	}

	tx, _ := store.Transaction(context.Background(), id)
	if !tx.Amount.Equal(decimal.RequireFromString("900")) {
		t.Errorf("сумма чужой записи изменилась на %s", tx.Amount)
	}
}

func TestDeleteAndRestore(t *testing.T) {
	store, base, client := loggedIn(t)
	id := addTx(t, store, testUserID, "600", "ошибка", classify.KindExpense, time.Now(), nil)
	url := base + "/api/transactions/" + itoa(int(id))

	resp, _ := send(t, client, http.MethodDelete, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("удаление: код %d", resp.StatusCode)
	}
	if _, err := store.Transaction(context.Background(), id); err == nil {
		t.Error("удалённая запись не должна читаться")
	}

	// «Вернуть» из тоста — тот же PATCH, отдельного эндпоинта не заводим.
	resp, body := send(t, client, http.MethodPatch, url, map[string]any{"deleted": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("возврат: код %d, тело %s", resp.StatusCode, body)
	}
	if _, err := store.Transaction(context.Background(), id); err != nil {
		t.Errorf("запись не вернулась: %v", err)
	}
}

func TestTransferDropsCategory(t *testing.T) {
	// Перевод — не трата на категорию (plan.md §3), правило одно на бота и веб.
	store, base, client := loggedIn(t)

	cats, _ := store.Categories(context.Background())
	food := cats[0].ID
	id := addTx(t, store, testUserID, "5000", "перевод", classify.KindExpense, time.Now(), &food)

	resp, body := send(t, client, http.MethodPatch, base+"/api/transactions/"+itoa(int(id)),
		map[string]any{"kind": classify.KindTransfer})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}

	var got txView
	_ = json.Unmarshal(body, &got)
	if got.CategoryID != nil {
		t.Errorf("у перевода осталась категория %v", *got.CategoryID)
	}
	if got.Beneficiary != classify.BenPartner {
		t.Errorf("бенефициар = %q, у перевода всегда партнёр", got.Beneficiary)
	}
}

func TestCreateBackdated(t *testing.T) {
	// Задача 5 из §1: добавить запись руками задним числом.
	store, base, client := loggedIn(t)
	cats, _ := store.Categories(context.Background())

	resp, body := send(t, client, http.MethodPost, base+"/api/transactions", map[string]any{
		"amount":      "1234.56",
		"description": "рынок",
		"category_id": cats[0].ID,
		"beneficiary": classify.BenBoth,
		"kind":        classify.KindExpense,
		"spent_at":    time.Now().AddDate(0, 0, -3).Format("2006-01-02"),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}

	var got txView
	_ = json.Unmarshal(body, &got)
	if got.Amount != "1234.56" || got.Description != "рынок" {
		t.Errorf("запись = %+v", got)
	}
	if got.Day != time.Now().AddDate(0, 0, -3).Format("2006-01-02") {
		t.Errorf("день = %s, ожидался позавчерашний", got.Day)
	}
}

func TestManualEditTeachesDictionary(t *testing.T) {
	// Правка категории на сайте должна учить словарь так же, как кнопка
	// бота: иначе быстрый путь зависит от того, где нажали (webapp.md §0).
	store, base, client := loggedIn(t)

	cats, _ := store.Categories(context.Background())
	var taxi int32
	for _, c := range cats {
		if c.Name == "Такси" {
			taxi = c.ID
		}
	}
	if _, err := store.Pool().Exec(context.Background(),
		`delete from word_map where user_id = $1`, testUserID); err != nil {
		t.Fatalf("очистка словаря: %v", err)
	}

	id := addTx(t, store, testUserID, "450", "самокат", classify.KindExpense, time.Now(), nil)
	resp, body := send(t, client, http.MethodPatch, base+"/api/transactions/"+itoa(int(id)),
		map[string]any{"category_id": taxi})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}

	hits, err := store.LookupWords(context.Background(), testUserID, []string{"самокат"})
	if err != nil {
		t.Fatalf("словарь: %v", err)
	}
	h, ok := hits["самокат"]
	if !ok || h.Source != storage.SourceManual || h.CategoryID != taxi {
		t.Errorf("слово в словаре = %+v, ожидалась ручная привязка к Такси", h)
	}
}

func TestBadPathRejected(t *testing.T) {
	_, base, client := loggedIn(t)

	resp, _ := send(t, client, http.MethodPatch, base+"/api/transactions/абв", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("код = %d, ожидался 400", resp.StatusCode)
	}
	resp, _ = send(t, client, http.MethodPatch, base+"/api/transactions/999999", map[string]any{"amount": "10"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("код = %d, ожидался 404", resp.StatusCode)
	}
}

func TestSecondEditWorks(t *testing.T) {
	// Версия уходит клиенту с той же точностью, с какой сверяется. Иначе
	// вторая правка записи вечно отвечает 409, и сохранить нельзя ничего.
	store, base, client := loggedIn(t)
	id := addTx(t, store, testUserID, "600", "тест", classify.KindExpense, time.Now(), nil)
	url := base + "/api/transactions/" + itoa(int(id))

	resp, body := send(t, client, http.MethodPatch, url, map[string]any{"amount": "700"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("первая правка: код %d, тело %s", resp.StatusCode, body)
	}
	var first txView
	_ = json.Unmarshal(body, &first)

	resp, body = send(t, client, http.MethodPatch, url,
		map[string]any{"amount": "800", "updated_at": *first.UpdatedAt})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("вторая правка: код %d, тело %s — версия не совпала сама с собой", resp.StatusCode, body)
	}

	var second txView
	_ = json.Unmarshal(body, &second)
	if second.Amount != "800" {
		t.Errorf("сумма = %s, ожидалось 800", second.Amount)
	}
}

func TestRestoreWorksAfterEdit(t *testing.T) {
	// «Вернуть» из тоста не может нести версию — и не должна её требовать.
	store, base, client := loggedIn(t)
	id := addTx(t, store, testUserID, "600", "тест", classify.KindExpense, time.Now(), nil)
	url := base + "/api/transactions/" + itoa(int(id))

	send(t, client, http.MethodPatch, url, map[string]any{"amount": "700"})
	send(t, client, http.MethodDelete, url, nil)

	resp, body := send(t, client, http.MethodPatch, url, map[string]any{"deleted": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("возврат правленой записи: код %d, тело %s", resp.StatusCode, body)
	}
	if _, err := store.Transaction(context.Background(), id); err != nil {
		t.Errorf("запись не вернулась: %v", err)
	}
}

func TestHugeAmountRejectedFast(t *testing.T) {
	// «1e2000000000» decimal разворачивает в гигабайты и кладёт процесс.
	_, base, client := loggedIn(t)

	done := make(chan int, 1)
	go func() {
		resp, _ := send(t, client, http.MethodPost, base+"/api/transactions",
			map[string]any{"amount": "1e2000000000", "description": "бомба", "spent_at": ""})
		done <- resp.StatusCode
	}()

	select {
	case code := <-done:
		if code != http.StatusBadRequest {
			t.Errorf("код = %d, ожидался 400", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("запрос не ответил за три секунды — сумма ушла в разбор")
	}
}

func TestAmountEdgeCases(t *testing.T) {
	store, base, client := loggedIn(t)
	id := addTx(t, store, testUserID, "600", "тест", classify.KindExpense, time.Now(), nil)
	url := base + "/api/transactions/" + itoa(int(id))

	// Меньше полкопейки — это не сумма, и отвечать надо внятно, а не 500.
	resp, _ := send(t, client, http.MethodPatch, url, map[string]any{"amount": "0.004"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("0.004 дало код %d, ожидался 400", resp.StatusCode)
	}
}

func TestEditingSumDoesNotTeachDictionary(t *testing.T) {
	// Словарь учится на смене категории, а не на правке суммы: ручная
	// привязка не перезатирается ничем, мусор в ней остаётся навсегда.
	store, base, client := loggedIn(t)

	cats, _ := store.Categories(context.Background())
	food := cats[0].ID
	if _, err := store.Pool().Exec(context.Background(),
		`delete from word_map where user_id = $1`, testUserID); err != nil {
		t.Fatalf("очистка словаря: %v", err)
	}

	id := addTx(t, store, testUserID, "600", "лимонад", classify.KindExpense, time.Now(), &food)
	send(t, client, http.MethodPatch, base+"/api/transactions/"+itoa(int(id)),
		map[string]any{"amount": "700"})

	hits, _ := store.LookupWords(context.Background(), testUserID, []string{"лимонад"})
	if _, ok := hits["лимонад"]; ok {
		t.Error("правка суммы не должна прибивать слово к категории навсегда")
	}
}

func TestBlindlyClassifiedDoesNotTeachDictionary(t *testing.T) {
	// У записи, разобранной вслепую, описание — это весь текст сообщения.
	// Учить по нему словарь нельзя ни из бота, ни с сайта.
	store, base, client := loggedIn(t)

	cats, _ := store.Categories(context.Background())
	var taxi, other int32
	for _, c := range cats {
		switch c.Name {
		case "Такси":
			taxi = c.ID
		case classify.CategoryOther:
			other = c.ID
		}
	}
	if _, err := store.Pool().Exec(context.Background(),
		`delete from word_map where user_id = $1`, testUserID); err != nil {
		t.Fatalf("очистка словаря: %v", err)
	}

	id, err := store.InsertTransaction(context.Background(), storage.Transaction{
		PayerID: testUserID, Beneficiary: classify.BenPayer, Kind: classify.KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "взял в дьюти фри",
		RawText: "взял в дьюти фри 600", NeedsClassification: true, SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	if _, err := store.MarkForReview(context.Background(), id, other); err != nil {
		t.Fatalf("пометка: %v", err)
	}

	send(t, client, http.MethodPatch, base+"/api/transactions/"+itoa(int(id)),
		map[string]any{"category_id": taxi})

	hits, _ := store.LookupWords(context.Background(), testUserID, []string{"взял", "дьюти", "фри"})
	if len(hits) != 0 {
		t.Errorf("в словарь уехали слова сырого текста: %v", hits)
	}
}

func TestClearingCategoryKeepsRecordInQueue(t *testing.T) {
	// Снятие категории не должно снимать запись с очереди разбора: наоборот,
	// разобрать её ещё нужно.
	store, base, client := loggedIn(t)

	id, err := store.InsertTransaction(context.Background(), storage.Transaction{
		PayerID: testUserID, Beneficiary: classify.BenPayer, Kind: classify.KindExpense,
		Amount: decimal.RequireFromString("600"), Description: "непонятное",
		RawText: "600 непонятное", NeedsClassification: true, SpentAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}

	send(t, client, http.MethodPatch, base+"/api/transactions/"+itoa(int(id)),
		map[string]any{"amount": "700", "clear_category": true})

	pending, err := store.PendingClassification(context.Background(), 10)
	if err != nil {
		t.Fatalf("очередь: %v", err)
	}
	found := false
	for _, tx := range pending {
		if tx.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("правка суммы сняла запись с очереди разбора — категории теперь не будет никогда")
	}
}
