package web

import (
	"net/http"

	"budget/internal/people"
)

// meResponse — кто смотрит и кто партнёр. Фронт по этому ответу назначает
// цветовые слоты: смотрящий всегда слот 1 (webapp-design.md §3.6).
type meResponse struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Dative  string `json:"dative"`
	Partner *user  `json:"partner"`
}

type user struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Dative — «Уле», «Илье». Подписи «на кого потрачено» читаются только
	// в дательном, а склонять имена на фронте — плодить вторую копию правил.
	Dative string `json:"dative"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	me := userID(r)

	users, err := s.store.Users(r.Context())
	if err != nil {
		s.log.Error("список пользователей", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}

	resp := meResponse{ID: me}
	for _, u := range users {
		switch {
		case u.ID == me:
			resp.Name, resp.Dative = u.Name, people.Dative(u.Name)
		case s.cfg.IsAllowed(u.ID) && resp.Partner == nil:
			resp.Partner = &user{ID: u.ID, Name: u.Name, Dative: people.Dative(u.Name)}
		}
	}
	if resp.Name == "" {
		resp.Name, resp.Dative = "Я", "мне"
	}
	writeJSON(w, http.StatusOK, resp)
}
