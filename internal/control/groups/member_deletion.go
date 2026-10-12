package groups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

func (s *Service) DeleteRetiredMember(ctx context.Context, adminID, groupID, nodeID string, expectedUpdatedAt time.Time, idempotencyKey string) (DeleteMemberResult, error) {
	adminID, groupID, nodeID, idempotencyKey = strings.TrimSpace(adminID), strings.TrimSpace(groupID), strings.TrimSpace(nodeID), strings.TrimSpace(idempotencyKey)
	if adminID == "" || groupID == "" || nodeID == "" || expectedUpdatedAt.IsZero() || !validIdempotencyKey(idempotencyKey) {
		return DeleteMemberResult{}, fmt.Errorf("%w: administrator, group_id, node_id, updated_at and valid Idempotency-Key are required", faults.ErrValidation)
	}
	repository, ok := s.repository.(RetiredMemberDeletionRepository)
	if !ok {
		return DeleteMemberResult{}, fmt.Errorf("%w: retired membership deletion is unavailable", faults.ErrValidation)
	}
	raw, err := json.Marshal(struct {
		GroupID   string    `json:"group_id"`
		NodeID    string    `json:"node_id"`
		UpdatedAt time.Time `json:"updated_at"`
	}{groupID, nodeID, expectedUpdatedAt.UTC()})
	if err != nil {
		return DeleteMemberResult{}, err
	}
	sum := sha256.Sum256(raw)
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", adminID, "device_group.member_delete", "device_group_member", groupID+":"+nodeID, "succeeded", nil)
	if err != nil {
		return DeleteMemberResult{}, err
	}
	return repository.DeleteRetiredGroupMember(ctx, DeleteMemberInput{
		GroupID: groupID, NodeID: nodeID, ExpectedUpdatedAt: expectedUpdatedAt.UTC(), DeletedAt: now,
		DeletedBy: adminID, IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(sum[:]),
	}, event)
}
