package web

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"budget/internal/storage"
)

type beneficiaryGroupView struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

type beneficiaryGroupBody struct {
	Name string `json:"name"`
}

const (
	maxBeneficiaryGroupName = 32
	maxBeneficiaryGroups    = 12
)

func groupView(g storage.BeneficiaryGroup) beneficiaryGroupView {
	return beneficiaryGroupView{ID: g.ID, Name: g.Name, Key: g.Key()}
}

func (s *Server) handleBeneficiaryGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.BeneficiaryGroups(r.Context())
	if err != nil {
		s.log.Error("группы получателей", "err", err)
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	out := make([]beneficiaryGroupView, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupView(g))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleBeneficiaryGroupCreate(w http.ResponseWriter, r *http.Request) {
	var body beneficiaryGroupBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "не разобрал запрос")
		return
	}
	name := trimTo(strings.TrimSpace(body.Name), maxBeneficiaryGroupName)
	if name == "" {
		writeError(w, http.StatusBadRequest, "у группы должно быть название")
		return
	}
	groups, err := s.store.BeneficiaryGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "база не отвечает")
		return
	}
	if len(groups) >= maxBeneficiaryGroups {
		writeError(w, http.StatusBadRequest, "групп уже слишком много")
		return
	}
	for _, g := range groups {
		if strings.EqualFold(g.Name, name) {
			writeError(w, http.StatusBadRequest, "группа с таким названием уже есть")
			return
		}
	}
	created, err := s.store.CreateBeneficiaryGroup(r.Context(), name)
	if err != nil {
		s.log.Error("создание группы получателей", "err", err)
		writeError(w, http.StatusInternalServerError, "не смог создать")
		return
	}
	writeJSON(w, http.StatusCreated, groupView(created))
}

func (s *Server) handleBeneficiaryGroupPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path)
	if !ok || id > math.MaxInt32 {
		writeError(w, http.StatusBadRequest, "не понял, какая это группа")
		return
	}
	var body beneficiaryGroupBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "не разобрал запрос")
		return
	}
	name := trimTo(strings.TrimSpace(body.Name), maxBeneficiaryGroupName)
	if name == "" {
		writeError(w, http.StatusBadRequest, "у группы должно быть название")
		return
	}
	updated, err := s.store.UpdateBeneficiaryGroup(r.Context(), int32(id), name)
	if err != nil {
		s.log.Error("правка группы получателей", "err", err)
		writeError(w, http.StatusInternalServerError, "не смог сохранить")
		return
	}
	writeJSON(w, http.StatusOK, groupView(updated))
}

func (s *Server) handleBeneficiaryGroupDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path)
	if !ok || id > math.MaxInt32 {
		writeError(w, http.StatusBadRequest, "не понял, какая это группа")
		return
	}
	deleted, used, err := s.store.DeleteBeneficiaryGroup(r.Context(), int32(id))
	if err != nil {
		s.log.Error("удаление группы получателей", "err", err)
		writeError(w, http.StatusInternalServerError, "не смог удалить")
		return
	}
	if used {
		writeError(w, http.StatusConflict, "группа уже используется в тратах или категориях")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "нет такой группы")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
