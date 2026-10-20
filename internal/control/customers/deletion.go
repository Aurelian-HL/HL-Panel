package customers

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type DeleteRequest struct {
	Revision int64 `json:"revision"`
}

type DeleteInput struct {
	ID, DeletedBy, IdempotencyKey, RequestSHA256 string
	ExpectedRevision                             int64
}

// DeletionRepository checks references and revision, removes sessions, and
// records the audit and replay result in one transaction. It never cascades
// into customer rules, subscriptions or other business records.
type DeletionRepository interface {
	DeleteCustomer(context.Context, DeleteInput, audit.Event) (bool, error)
	DeleteUserGroup(context.Context, DeleteInput, audit.Event) (bool, error)
}

func (s *Service) DeleteCustomer(ctx context.Context, adminID, id string, request DeleteRequest, key string) (bool, error) {
	return s.delete(ctx, adminID, id, request, key, "customer")
}

func (s *Service) DeleteUserGroup(ctx context.Context, adminID, id string, request DeleteRequest, key string) (bool, error) {
	return s.delete(ctx, adminID, id, request, key, "user_group")
}

func (s *Service) delete(ctx context.Context, adminID, id string, request DeleteRequest, key, kind string) (bool, error) {
	if !validID(id) || !validID(adminID) {
		return false, fmt.Errorf("%w: invalid deletion resource or administrator ID", faults.ErrValidation)
	}
	if err := validateMutation(key, request.Revision, false); err != nil {
		return false, err
	}
	hash, err := requestFingerprint(struct {
		ID       string
		Revision int64
	}{id, request.Revision})
	if err != nil {
		return false, err
	}
	event, err := audit.NewEvent(s.now().UTC(), "administrator", adminID, kind+".delete", kind, id, "succeeded", map[string]any{"revision": request.Revision})
	if err != nil {
		return false, err
	}
	repo, ok := s.repository.(DeletionRepository)
	if !ok {
		return false, fmt.Errorf("%w: deletion is unavailable", faults.ErrConflict)
	}
	input := DeleteInput{ID: id, DeletedBy: adminID, ExpectedRevision: request.Revision, IdempotencyKey: key, RequestSHA256: hash}
	if kind == "customer" {
		return repo.DeleteCustomer(ctx, input, event)
	}
	return repo.DeleteUserGroup(ctx, input, event)
}
