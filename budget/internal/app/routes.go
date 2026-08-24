package app

import (
	"net/http"
)

// routes — вся поверхность приложения.
//
// Каждый маршрут проходит authenticate: обходной двери нет ни у одного, и
// добавить её случайно нельзя — не обёрнутый обработчик просто не попадёт
// в этот список.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	get := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc("GET "+pattern, s.authenticate(h))
	}
	post := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc("POST "+pattern, s.authenticate(h))
	}
	patch := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc("PATCH "+pattern, s.authenticate(h))
	}
	del := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc("DELETE "+pattern, s.authenticate(h))
	}

	// Состояние: кто я, в какой группе, кто ещё в ней и какие категории.
	// Одним запросом, а не пятью: приложение открывают на телефоне, и пять
	// последовательных round-trip по мобильной сети — это заметная пауза.
	get("/api/state", s.state)

	post("/api/me/name", s.setName)
	post("/api/me/avatar", s.setAvatar)
	del("/api/me/avatar", s.clearAvatar)
	get("/api/avatar/{id}", s.avatar)

	post("/api/group", s.createGroup)
	post("/api/group/invite", s.invite)
	post("/api/group/leave", s.leave)
	patch("/api/group/members/{id}", s.setRole)
	del("/api/group/members/{id}", s.removeMember)

	post("/api/invites/{id}/accept", s.acceptInvite)
	post("/api/invites/{id}/decline", s.declineInvite)

	get("/api/transactions", s.listTransactions)
	patch("/api/transactions/{id}", s.updateTransaction)
	del("/api/transactions/{id}", s.deleteTransaction)
	post("/api/transactions/{id}/restore", s.restoreTransaction)

	post("/api/categories", s.createCategory)
	patch("/api/categories/{id}", s.updateCategory)

	get("/api/report/month", s.monthReport)
	get("/api/report/days", s.dayReport)
	get("/api/report/months", s.monthStrip)

	// Статика фронта — без подписи: это просто файлы, а всё, что за ними,
	// закрыто API. Проверять подпись у index.html нельзя: Telegram открывает
	// его обычным GET, до того как приложение вообще узнает про initData.
	mux.Handle("/", assets())

	return mux
}
