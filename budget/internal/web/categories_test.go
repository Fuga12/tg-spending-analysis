package web

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Адресатом категории может быть только участник бюджета: чужой id означал бы
// траты на того, кого в бюджете нет.
func TestCategoryPersonMustBeInBudget(t *testing.T) {
	_, base, client := loggedIn(t)

	resp, body := send(t, client, http.MethodPost, base+"/api/categories",
		map[string]any{"name": "Косметика", "hint": "уход", "beneficiary": "payer", "user_id": testUserID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}
	var created categoryView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if created.UserID == nil || *created.UserID != testUserID {
		t.Errorf("адресат = %v, ожидался участник бюджета", created.UserID)
	}

	resp, body = send(t, client, http.MethodPost, base+"/api/categories",
		map[string]any{"name": "Чужая", "beneficiary": "payer", "user_id": 999999})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("код = %d, чужой адресат должен отвергаться (тело %s)", resp.StatusCode, body)
	}

	// null снимает адресата и возвращает относительное умолчание.
	resp, body = send(t, client, http.MethodPatch, base+"/api/categories/"+itoa(int(created.ID)),
		map[string]any{"name": "Косметика", "hint": "уход", "beneficiary": "both", "user_id": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}
	var updated categoryView
	_ = json.Unmarshal(body, &updated)
	if updated.UserID != nil {
		t.Errorf("адресат = %v, ожидалось снятие", *updated.UserID)
	}
}

// Кто смотрит и кто партнёр — с именами в дательном падеже: подписи «на кого»
// собираются из них, и склонять их на фронте значило бы держать вторую копию
// правил.
func TestMeReturnsDativeNames(t *testing.T) {
	_, base, client := loggedIn(t)

	resp, body := send(t, client, http.MethodGet, base+"/api/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код = %d, тело %s", resp.StatusCode, body)
	}
	var me meResponse
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if me.Dative != "Тесту" {
		t.Errorf("дательный = %q, ожидалось «Тесту» от имени «Тест»", me.Dative)
	}
}
