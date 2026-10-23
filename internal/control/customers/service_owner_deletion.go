package customers

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

// ServiceOwnerDeletionRepository removes only an unreferenced non-login owner.
// Revision, references, sessions, audit and replay must share one transaction.
// This is distinct from DeletionRepository, which revokes panel login only.
type ServiceOwnerDeletionRepository interface {
	DeleteServiceOwner(context.Context, DeleteInput, audit.Event) (bool, error)
}

func (s *Service) DeleteServiceOwner(ctx context.Context, adminID, id string, request DeleteRequest, key string) (bool, error) {
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
	event, err := audit.NewEvent(s.now().UTC(), "administrator", adminID, "service_owner.delete", "service_owner", id, "succeeded", map[string]any{"revision": request.Revision})
	if err != nil {
		return false, err
	}
	repo, ok := s.repository.(ServiceOwnerDeletionRepository)
	if !ok {
		return false, fmt.Errorf("%w: service owner deletion is unavailable", faults.ErrConflict)
	}
	return repo.DeleteServiceOwner(ctx, DeleteInput{
		ID: id, DeletedBy: adminID, ExpectedRevision: request.Revision,
		IdempotencyKey: key, RequestSHA256: hash,
	}, event)
}
