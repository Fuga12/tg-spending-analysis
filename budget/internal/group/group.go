// Package group — действия над составом группы вместе с их последствиями
// снаружи базы.
//
// Хранилище умеет позвать человека в группу, но не умеет ему об этом
// сказать. Приглашение, о котором никого не уведомили, невидимо: приложение
// показывает его только тому, кто уже догадался открыть нужный экран.
// Поэтому пара «пригласить и уведомить» живёт здесь, а не расходится по
// вызывающим — забыть половину слишком легко.
package group

import (
	"context"
	"log/slog"

	"budget/internal/storage"
)

// Notifier — как сказать человеку, что его позвали. В боевом коде это бот.
type Notifier interface {
	InviteReceived(inv storage.Invite) error
}

// NotifyFunc — уведомление одной функцией.
//
// Нужен из-за порядка сборки: бот появляется позже сервиса, потому что сам
// в него ходит. Замыкание связывает их, не заводя ради этого изменяемого
// поля, которое кто-нибудь однажды забудет проставить.
type NotifyFunc func(storage.Invite) error

func (f NotifyFunc) InviteReceived(inv storage.Invite) error { return f(inv) }

// Service связывает хранилище с уведомлениями.
type Service struct {
	store  *storage.Store
	notify Notifier
	log    *slog.Logger
}

func New(store *storage.Store, notify Notifier, log *slog.Logger) *Service {
	return &Service{store: store, notify: notify, log: log}
}

// Create заводит группу и делает создателя её администратором.
func (s *Service) Create(ctx context.Context, name string, ownerUserID int64) (storage.Group, storage.Member, error) {
	return s.store.CreateGroup(ctx, name, ownerUserID)
}

// Invite зовёт человека в группу и сразу пишет ему об этом.
//
// Неудача уведомления не отменяет приглашение: оно уже записано и остаётся
// действительным — человек увидит его в приложении. Молчать об этом всё же
// нельзя, поэтому в лог.
func (s *Service) Invite(ctx context.Context, groupID, byUserID, inviteeUserID int64) (storage.Invite, error) {
	inv, err := s.store.ForGroup(groupID).Invite(ctx, byUserID, inviteeUserID)
	if err != nil {
		return storage.Invite{}, err
	}
	if err := s.notify.InviteReceived(inv); err != nil {
		s.log.Warn("не уведомил о приглашении", "err", err,
			"invite", inv.ID, "user_id", inviteeUserID)
	}
	return inv, nil
}

// Accept вводит приглашённого в группу.
func (s *Service) Accept(ctx context.Context, inviteeUserID, inviteID int64) (storage.Member, error) {
	return s.store.AcceptInvite(ctx, inviteeUserID, inviteID)
}

// Decline отказывается от приглашения.
func (s *Service) Decline(ctx context.Context, inviteeUserID, inviteID int64) error {
	return s.store.DeclineInvite(ctx, inviteeUserID, inviteID)
}

// Pending — открытые приглашения человека.
func (s *Service) Pending(ctx context.Context, userID int64) ([]storage.Invite, error) {
	return s.store.PendingInvites(ctx, userID)
}

// Leave — участник уходит из группы сам.
func (s *Service) Leave(ctx context.Context, groupID, userID int64) error {
	return s.store.ForGroup(groupID).Leave(ctx, userID)
}

// Remove — администратор исключает участника.
func (s *Service) Remove(ctx context.Context, groupID, byUserID, memberID int64) error {
	return s.store.ForGroup(groupID).RemoveMember(ctx, byUserID, memberID)
}

// SetRole повышает участника до администратора или разжалует обратно.
func (s *Service) SetRole(ctx context.Context, groupID, byUserID, memberID int64, role string) error {
	return s.store.ForGroup(groupID).SetRole(ctx, byUserID, memberID, role)
}
