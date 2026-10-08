package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/securetoken"
)

type DeleteInput struct {
	NodeID, DeletedBy, IdempotencyKey, RequestSHA256 string
	DeletedAt                                        time.Time
	OnlineFor                                        time.Duration
}

type DeleteResult struct {
	NodeID    string    `json:"node_id"`
	DeletedAt time.Time `json:"deleted_at"`
}

func (s *Service) DeleteOffline(ctx context.Context, administratorID, nodeID, key string) (DeleteResult, bool, error) {
	administratorID, nodeID, key = strings.TrimSpace(administratorID), strings.TrimSpace(nodeID), strings.TrimSpace(key)
	if !validIdentifier(administratorID) || !validIdentifier(nodeID) || !validIdempotencyKey(key) {
		return DeleteResult{}, false, fmt.Errorf("%w: node, administrator, and Idempotency-Key are required", faults.ErrValidation)
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", administratorID, "node.delete", "node", nodeID, "succeeded", map[string]any{"history_preserved": true})
	if err != nil {
		return DeleteResult{}, false, err
	}
	digest := sha256.Sum256([]byte("node.delete\x00" + nodeID))
	return s.repository.DeleteOffline(ctx, DeleteInput{NodeID: nodeID, DeletedBy: administratorID, IdempotencyKey: key, RequestSHA256: hex.EncodeToString(digest[:]), DeletedAt: now, OnlineFor: s.onlineFor}, event)
}

// Recovering a deleted identity requires its private credential AND a fresh
// administrator-issued group token. Ordinary agent endpoints reject it.
func (s *Service) AuthenticateReenrollmentCredential(ctx context.Context, credential string) (Node, error) {
	if credential == "" {
		return Node{}, faults.ErrUnauthorized
	}
	return s.repository.NodeByCredentialHash(ctx, securetoken.Hash(credential))
}
