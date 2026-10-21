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

// CustomerDeletionRepository atomically checks references and revision, removes
// sessions, and records the audit and replay result. Deletion never cascades into
// rules, subscriptions or historical business records.
type CustomerDeletionRepository interface {
	DeleteCustomer(context.Context, DeleteInput, audit.Event) (bool, error)
}

func (s *Service) DeleteCustomer(ctx context.Context, adminID, id string, request DeleteRequest, key string) (bool, error) {
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
	event, err := audit.NewEvent(s.now().UTC(), "administrator", adminID, "customer.delete", "customer", id, "succeeded", map[string]any{"revision": request.Revision})
	if err != nil {
		return false, err
	}
	repository, ok := s.repository.(CustomerDeletionRepository)
	if !ok {
		return false, fmt.Errorf("%w: customer deletion is unavailable", faults.ErrConflict)
	}
	return repository.DeleteCustomer(ctx, DeleteInput{
		ID: id, DeletedBy: adminID, ExpectedRevision: request.Revision, IdempotencyKey: key, RequestSHA256: hash,
	}, event)
}
