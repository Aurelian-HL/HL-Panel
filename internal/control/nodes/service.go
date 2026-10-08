package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/securetoken"
)

type Service struct {
	repository Repository
	now        func() time.Time
	onlineFor  time.Duration
}

func NewService(repository Repository, now func() time.Time, onlineFor time.Duration) *Service {
	if now == nil {
		now = time.Now
	}
	if onlineFor <= 0 {
		onlineFor = 90 * time.Second
	}
	return &Service{repository: repository, now: now, onlineFor: onlineFor}
}

func (s *Service) AuthenticateCredential(ctx context.Context, rawCredential string) (Node, error) {
	if rawCredential == "" {
		return Node{}, faults.ErrUnauthorized
	}
	node, err := s.repository.NodeByCredentialHash(ctx, securetoken.Hash(rawCredential))
	if err != nil {
		return Node{}, err
	}
	if node.DeletedAt != nil {
		return Node{}, faults.ErrUnauthorized
	}
	return node, nil
}

// RotateCredential atomically invalidates the current node credential and
// returns a replacement exactly once. Replaying the same idempotency key is
// safe, but cannot reveal the replacement again because it is not persisted.
func (s *Service) RotateCredential(ctx context.Context, administratorID, nodeID, idempotencyKey string) (CredentialRotationResult, bool, error) {
	administratorID = strings.TrimSpace(administratorID)
	nodeID = strings.TrimSpace(nodeID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validIdentifier(administratorID) || !validIdentifier(nodeID) || !validIdempotencyKey(idempotencyKey) {
		return CredentialRotationResult{}, false, fmt.Errorf("%w: node, administrator, and Idempotency-Key are required", faults.ErrValidation)
	}
	rawCredential, err := securetoken.Generate("node")
	if err != nil {
		return CredentialRotationResult{}, false, fmt.Errorf("generate node credential: %w", err)
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", administratorID, "node.credential.rotate", "node", nodeID, "succeeded", map[string]any{"credential_replaced": true})
	if err != nil {
		return CredentialRotationResult{}, false, err
	}
	result, replayed, err := s.repository.RotateCredential(ctx, RotateCredentialInput{
		NodeID: nodeID, CredentialHash: securetoken.Hash(rawCredential), UpdatedBy: administratorID,
		IdempotencyKey: idempotencyKey, RequestSHA256: rotationDigest(nodeID), UpdatedAt: now,
	}, event)
	if err != nil {
		return CredentialRotationResult{}, false, err
	}
	if !replayed {
		result.NodeCredential = rawCredential
	}
	return result, replayed, nil
}

func (s *Service) List(ctx context.Context) ([]View, error) {
	items, err := s.repository.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	views := make([]View, 0, len(items))
	for _, node := range items {
		views = append(views, View{Node: node, Status: nodeStatus(node, now, s.onlineFor)})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Name == views[j].Name {
			return views[i].ID < views[j].ID
		}
		return views[i].Name < views[j].Name
	})
	return views, nil
}

func (s *Service) RecordHeartbeat(ctx context.Context, nodeID string, heartbeat Heartbeat) (Node, error) {
	for fieldName, fieldValue := range map[string]string{
		"hostname":     heartbeat.Hostname,
		"platform":     heartbeat.Platform,
		"architecture": heartbeat.Architecture,
		"boot_id":      heartbeat.BootID,
	} {
		if strings.TrimSpace(fieldValue) == "" || len(fieldValue) > 255 {
			return Node{}, fmt.Errorf("%w: %s is required and must be at most 255 characters", faults.ErrValidation, fieldName)
		}
	}
	if strings.TrimSpace(heartbeat.BootID) == "" {
		return Node{}, fmt.Errorf("%w: boot_id is required", faults.ErrValidation)
	}
	if heartbeat.CurrentAppliedGeneration < 0 {
		return Node{}, fmt.Errorf("%w: current_applied_generation must be non-negative", faults.ErrValidation)
	}
	if len(heartbeat.Capabilities) > 128 {
		return Node{}, fmt.Errorf("%w: too many capabilities", faults.ErrValidation)
	}
	if !validApplyStatus(heartbeat.LastApplyStatus) {
		return Node{}, fmt.Errorf("%w: invalid last_apply_status", faults.ErrValidation)
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "node", nodeID, "node.heartbeat", "node", nodeID, "succeeded", map[string]any{
		"boot_id": heartbeat.BootID,
	})
	if err != nil {
		return Node{}, err
	}
	return s.repository.UpdateHeartbeat(ctx, nodeID, heartbeat, now, event)
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	now := s.now().UTC()
	overview, err := s.repository.Overview(ctx, now, s.onlineFor)
	if err != nil {
		return Overview{}, err
	}
	items, err := s.repository.ListNodes(ctx)
	if err != nil {
		return Overview{}, err
	}
	overview.Nodes = make([]View, 0, len(items))
	for _, node := range items {
		overview.Nodes = append(overview.Nodes, View{Node: node, Status: nodeStatus(node, now, s.onlineFor)})
	}
	sort.Slice(overview.Nodes, func(i, j int) bool {
		if overview.Nodes[i].Name == overview.Nodes[j].Name {
			return overview.Nodes[i].ID < overview.Nodes[j].ID
		}
		return overview.Nodes[i].Name < overview.Nodes[j].Name
	})
	return overview, nil
}

func (s *Service) RequestControl(ctx context.Context, administratorID, nodeID, command, idempotencyKey string) (ControlCommandResult, bool, error) {
	command = strings.TrimSpace(command)
	if !validIdentifier(administratorID) || !validIdentifier(nodeID) || !validIdentifier(idempotencyKey) {
		return ControlCommandResult{}, false, fmt.Errorf("%w: node, administrator, and Idempotency-Key are required", faults.ErrValidation)
	}
	if !validControlCommand(command) {
		return ControlCommandResult{}, false, fmt.Errorf("%w: unsupported node control command", faults.ErrValidation)
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", administratorID, "node.control."+command, "node", nodeID, "succeeded", map[string]any{"command": command})
	if err != nil {
		return ControlCommandResult{}, false, err
	}
	return s.repository.RequestControl(ctx, ControlCommandInput{NodeID: nodeID, Command: command, AdministratorID: administratorID, IdempotencyKey: idempotencyKey, RequestSHA256: rotationDigest(nodeID + "\x00" + command), CommandID: newControlID(now), UpdatedAt: now}, event)
}

func (s *Service) ControlForNode(ctx context.Context, nodeID string) (ControlCommandResult, error) {
	return s.repository.ControlForNode(ctx, nodeID)
}

func (s *Service) RecordControlResult(ctx context.Context, nodeID string, result ControlCommandResult) error {
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "node", nodeID, "node.control.result", "node", nodeID, result.Status, map[string]any{"command": result.Command, "status": result.Status})
	if err != nil {
		return err
	}
	result.UpdatedAt = now
	return s.repository.RecordControlResult(ctx, nodeID, result, event)
}

func validControlCommand(command string) bool {
	switch command {
	case "status", "logs", "stop", "restart", "version":
		return true
	default:
		return false
	}
}

func newControlID(now time.Time) string { return fmt.Sprintf("control_%d", now.UnixNano()) }

func nodeStatus(node Node, now time.Time, onlineFor time.Duration) string {
	if node.LastHeartbeatAt == nil || now.Sub(*node.LastHeartbeatAt) > onlineFor {
		return "offline"
	}
	if node.LastApplyStatus == "failed" {
		return "failed"
	}
	if node.DesiredGeneration > node.AppliedGeneration {
		return "syncing"
	}
	return "online"
}

func validApplyStatus(status string) bool {
	switch status {
	case "", "succeeded", "failed", "rolled_back":
		return true
	default:
		return false
	}
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validIdempotencyKey(value string) bool { return validIdentifier(value) }

func rotationDigest(nodeID string) string {
	hash := sha256.Sum256([]byte("node.credential.rotate\x00" + nodeID))
	return hex.EncodeToString(hash[:])
}
