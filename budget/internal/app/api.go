package app

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"budget/internal/storage"
)

// stateResponse — всё, что нужно приложению, чтобы отрисоваться.
type stateResponse struct {
	Me         userJSON     `json:"me"`
	Group      *groupJSON   `json:"group"`
	Members    []memberJSON `json:"members"`
	Categories []categJSON  `json:"categories"`
	Invites    []inviteJSON `json:"invites"`
	// MaxMembers — потолок группы. Приложение по нему гасит «пригласить»
	// заранее, а не после отказа сервера.
	MaxMembers int `json:"max_members"`
}

type userJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	AvatarAt string `json:"avatar_at,omitempty"`
}

type groupJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// MemberID и Role — про смотрящего: приложение по ним решает, показывать
	// ли «пригласить» и «исключить».
	MemberID int64  `json:"member_id"`
	Role     string `json:"role"`
}

type memberJSON struct {
	ID       int64  `json:"id"`
	UserID   int64  `json:"user_id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Left     bool   `json:"left,omitempty"`
	AvatarAt string `json:"avatar_at,omitempty"`
}

type categJSON struct {
	ID          int32  `json:"id"`
	Name        string `json:"name"`
	Hint        string `json:"hint"`
	TemplateKey string `json:"template_key,omitempty"`
	DefaultTo   *int64 `json:"default_to"`
	SortOrder   int    `json:"sort_order"`
}

type inviteJSON struct {
	ID        int64  `json:"id"`
	GroupName string `json:"group_name"`
	Inviter   string `json:"inviter"`
	ExpiresAt string `json:"expires_at"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r)
	ctx := r.Context()

	out := stateResponse{
		Me:         toUser(c.user),
		Members:    []memberJSON{},
		Categories: []categJSON{},
		Invites:    []inviteJSON{},
		MaxMembers: storage.MaxGroupSize,
	}

	if !c.inGroup {
		// Человек без группы видит только свои приглашения: это весь его
		// выбор — принять одно или завести свою.
		invites, err := s.store.PendingInvites(ctx, c.user.ID)
		if err != nil {
			s.oops(w, "открытые приглашения", err)
			return
		}
		for _, inv := range invites {
			out.Invites = append(out.Invites, toInvite(inv))
		}
		ok(w, out)
		return
	}

	// Участники — вместе с ушедшими: их траты остались в истории, и подписать
	// их надо, иначе в отчёте появится «Кто-то ещё».
	members, err := c.group.AllMembers(ctx)
	if err != nil {
		s.oops(w, "участники", err)
		return
	}
	avatars, err := s.store.AvatarVersions(ctx, userIDsOf(members))
	if err != nil {
		s.oops(w, "аватарки", err)
		return
	}
	for _, m := range members {
		out.Members = append(out.Members, toMember(m, avatars[m.UserID]))
	}

	cats, err := c.group.Categories(ctx)
	if err != nil {
		s.oops(w, "категории", err)
		return
	}
	for _, cat := range cats {
		out.Categories = append(out.Categories, toCategory(cat))
	}

	g, err := c.group.Group(ctx)
	if err != nil {
		s.oops(w, "группа", err)
		return
	}
	out.Group = &groupJSON{ID: g.ID, Name: g.Name, MemberID: c.member.ID, Role: c.member.Role}
	ok(w, out)
}

// --- профиль ---

func (s *Server) setName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		fail(w, http.StatusBadRequest, "Имя не может быть пустым.")
		return
	}
	if err := s.store.SetName(r.Context(), callerFrom(r).user.ID, name); err != nil {
		s.oops(w, "переименование", err)
		return
	}
	ok(w, nil)
}

func (s *Server) setAvatar(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	// Картинка маленькая: 256×256 JPEG — это десятки килобайт. Потолок стоит
	// не ради места, а чтобы дамп базы не распух незаметно.
	data, err := readLimited(w, r, 512<<10)
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "Фото слишком большое.")
		return
	}
	at, err := s.store.SetAvatar(r.Context(), callerFrom(r).user.ID, data)
	if err != nil {
		s.oops(w, "фото профиля", err)
		return
	}
	ok(w, map[string]string{"avatar_at": at.UTC().Format(time.RFC3339Nano)})
}

func (s *Server) clearAvatar(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ClearAvatar(r.Context(), callerFrom(r).user.ID); err != nil {
		s.oops(w, "сброс фото", err)
		return
	}
	ok(w, nil)
}

// avatar отдаёт фото участника. Видеть чужие фото вправе только тот, кто
// с этим человеком в одной группе.
func (s *Server) avatar(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, http.StatusBadRequest, "Не понял, чьё фото.")
		return
	}
	if id != c.user.ID {
		if !c.inGroup {
			fail(w, http.StatusForbidden, "Не твоя группа.")
			return
		}
		members, err := c.group.AllMembers(r.Context())
		if err != nil {
			s.oops(w, "участники", err)
			return
		}
		if !hasUser(members, id) {
			fail(w, http.StatusForbidden, "Не твоя группа.")
			return
		}
	}

	data, err := s.store.Avatar(r.Context(), id)
	if err != nil {
		s.oops(w, "фото", err)
		return
	}
	if len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	// Версия картинки уезжает в адрес, поэтому кэшировать можно надолго.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data)
}

// --- группа ---

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	c := callerFrom(r)
	if c.inGroup {
		fail(w, http.StatusConflict, "Ты уже в группе.")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Бюджет"
	}
	g, _, err := s.groups.Create(r.Context(), name, c.user.ID)
	if err != nil {
		s.oops(w, "создание группы", err)
		return
	}
	ok(w, map[string]any{"id": g.ID, "name": g.Name})
}

func (s *Server) invite(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		var body struct {
			UserID int64 `json:"user_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		inv, err := s.groups.Invite(r.Context(), c.member.GroupID, c.user.ID, body.UserID)
		if err != nil {
			s.groupError(w, "приглашение", err)
			return
		}
		ok(w, toInvite(inv))
	})
}

func (s *Server) leave(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		if err := s.groups.Leave(r.Context(), c.member.GroupID, c.user.ID); err != nil {
			s.groupError(w, "выход", err)
			return
		}
		ok(w, nil)
	})
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		id, okID := pathID(w, r)
		if !okID {
			return
		}
		var body struct {
			Role string `json:"role"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := c.group.SetRole(r.Context(), c.user.ID, id, body.Role); err != nil {
			s.groupError(w, "смена роли", err)
			return
		}
		ok(w, nil)
	})
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		id, okID := pathID(w, r)
		if !okID {
			return
		}
		if err := c.group.RemoveMember(r.Context(), c.user.ID, id); err != nil {
			s.groupError(w, "исключение", err)
			return
		}
		ok(w, nil)
	})
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	m, err := s.groups.Accept(r.Context(), callerFrom(r).user.ID, id)
	if err != nil {
		s.groupError(w, "принятие приглашения", err)
		return
	}
	ok(w, map[string]any{"group_id": m.GroupID})
}

func (s *Server) declineInvite(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	if err := s.groups.Decline(r.Context(), callerFrom(r).user.ID, id); err != nil {
		s.groupError(w, "отказ от приглашения", err)
		return
	}
	ok(w, nil)
}

// --- категории ---

func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		var body struct {
			Name      string `json:"name"`
			Hint      string `json:"hint"`
			DefaultTo *int64 `json:"default_to"`
		}
		if !decode(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			fail(w, http.StatusBadRequest, "У категории должно быть название.")
			return
		}
		cat, err := c.group.CreateCategory(r.Context(), body.Name, body.Hint, body.DefaultTo)
		if err != nil {
			s.oops(w, "создание категории", err)
			return
		}
		s.forgetGuesses(r, c)
		ok(w, toCategory(cat))
	})
}

func (s *Server) updateCategory(w http.ResponseWriter, r *http.Request) {
	inGroup(s, w, r, func(c *caller) {
		id, okID := pathID(w, r)
		if !okID {
			return
		}
		var body struct {
			Name      string `json:"name"`
			Hint      string `json:"hint"`
			DefaultTo *int64 `json:"default_to"`
		}
		if !decode(w, r, &body) {
			return
		}
		changed, err := c.group.UpdateCategory(r.Context(), int32(id), body.Name, body.Hint, body.DefaultTo)
		if err != nil {
			s.oops(w, "правка категории", err)
			return
		}
		if !changed {
			http.NotFound(w, r)
			return
		}
		s.forgetGuesses(r, c)
		ok(w, nil)
	})
}

// forgetGuesses выбрасывает догадки модели из словарей группы: список
// категорий изменился, и прежние привязки устарели. «Пиво», однажды
// разобранное в Продукты, иначе резолвилось бы туда и после появления
// категории «Алкоголь». Ручные привязки не трогаются.
func (s *Server) forgetGuesses(r *http.Request, c *caller) {
	n, err := c.group.ForgetLLMWords(r.Context())
	if err != nil {
		s.log.Warn("не сбросил словарь после правки категорий", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("словарь сброшен после правки категорий", "слов", n, "group", c.member.GroupID)
	}
}

// --- вспомогательное ---

// inGroup — обёртка для маршрутов, которым нужна группа.
func inGroup(s *Server, w http.ResponseWriter, r *http.Request, next func(*caller)) {
	c := callerFrom(r)
	if !c.inGroup {
		fail(w, http.StatusPreconditionRequired, "Ты пока не в группе.")
		return
	}
	next(c)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, "Не понял, о чём речь.")
		return 0, false
	}
	return id, true
}

// oops — неожиданная поломка: клиенту общая фраза, подробности в лог.
func (s *Server) oops(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	fail(w, http.StatusServiceUnavailable, "База не отвечает, попробуй ещё раз.")
}

// groupError переводит отказ правил группы в ответ, который можно показать.
// Каждая причина означает разное, и общая фраза «не получилось» заставляет
// человека гадать, что он сделал не так.
func (s *Server) groupError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, storage.ErrNotAdmin):
		fail(w, http.StatusForbidden, "Это может только администратор.")
	case errors.Is(err, storage.ErrNotMember):
		fail(w, http.StatusNotFound, "Такого участника в группе нет.")
	case errors.Is(err, storage.ErrLastAdmin):
		fail(w, http.StatusConflict, "Ты последний администратор — сначала назначь другого.")
	case errors.Is(err, storage.ErrGroupFull):
		fail(w, http.StatusConflict, "В группе больше нет места.")
	case errors.Is(err, storage.ErrAlreadyMember):
		fail(w, http.StatusConflict, "Человек уже состоит в группе.")
	case errors.Is(err, storage.ErrUnknownUser):
		fail(w, http.StatusNotFound, "Этот человек ещё не открывал бота — пусть напишет ему /start.")
	case errors.Is(err, storage.ErrNoInvite):
		fail(w, http.StatusNotFound, "Приглашение не найдено.")
	case errors.Is(err, storage.ErrInviteExpired):
		fail(w, http.StatusGone, "Приглашение просрочено — попроси новое.")
	case errors.Is(err, storage.ErrBadRole):
		fail(w, http.StatusBadRequest, "Такой роли нет.")
	default:
		s.oops(w, what, err)
	}
}

func toUser(u storage.User) userJSON {
	out := userJSON{ID: u.ID, Name: u.Name}
	if u.AvatarAt != nil {
		out.AvatarAt = u.AvatarAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func toMember(m storage.Member, avatarAt *time.Time) memberJSON {
	out := memberJSON{ID: m.ID, UserID: m.UserID, Name: m.Name, Role: m.Role, Left: m.LeftAt != nil}
	if avatarAt != nil {
		out.AvatarAt = avatarAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func toCategory(c storage.Category) categJSON {
	return categJSON{
		ID: c.ID, Name: c.Name, Hint: c.Hint,
		TemplateKey: c.TemplateKey, DefaultTo: c.DefaultMemberID, SortOrder: c.SortOrder,
	}
}

func toInvite(inv storage.Invite) inviteJSON {
	return inviteJSON{
		ID: inv.ID, GroupName: inv.GroupName, Inviter: inv.InviterName,
		ExpiresAt: inv.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func userIDsOf(members []storage.Member) []int64 {
	out := make([]int64, 0, len(members))
	for _, m := range members {
		out = append(out, m.UserID)
	}
	return out
}

func hasUser(members []storage.Member, userID int64) bool {
	for _, m := range members {
		if m.UserID == userID {
			return true
		}
	}
	return false
}

// readLimited читает тело с потолком на размер.
func readLimited(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, max))
}

// parseAmount читает сумму из строки: в JSON число — это float64, а деньги
// через float гонять нельзя.
func parseAmount(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(strings.TrimSpace(s))
}
