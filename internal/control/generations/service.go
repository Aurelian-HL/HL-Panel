package generations

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const maxConfigBytes = 1 << 20

type Service struct {
	repository   Repository
	now          func() time.Time
	onDeployment func(string)
}

type ServiceOption func(*Service)

// WithDeploymentNotification schedules protocol verification after a durable,
// successful engine verification. It never grants health by itself.
func WithDeploymentNotification(notify func(string)) ServiceOption {
	return func(s *Service) { s.onDeployment = notify }
}

func NewService(repository Repository, now func() time.Time, options ...ServiceOption) *Service {
	if now == nil {
		now = time.Now
	}
	s := &Service{repository: repository, now: now}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Service) CreateGroupRevision(ctx context.Context, adminID, groupID string, engine agentv1.Engine, rawConfig json.RawMessage, idempotencyKey string) (CreateGroupRevisionResult, error) {
	if strings.TrimSpace(groupID) == "" {
		return CreateGroupRevisionResult{}, fmt.Errorf("%w: group_id is required", faults.ErrValidation)
	}
	if engine != agentv1.EngineXray && engine != agentv1.EngineGOST {
		return CreateGroupRevisionResult{}, fmt.Errorf("%w: unsupported group engine", faults.ErrValidation)
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return CreateGroupRevisionResult{}, fmt.Errorf("%w: idempotency_key must contain 1 to 128 characters", faults.ErrValidation)
	}
	if len(rawConfig) == 0 || len(rawConfig) > maxConfigBytes {
		return CreateGroupRevisionResult{}, fmt.Errorf("%w: config must contain at most %d bytes", faults.ErrValidation, maxConfigBytes)
	}
	canonical, err := CanonicalJSON(rawConfig)
	if err != nil {
		return CreateGroupRevisionResult{}, err
	}
	id, err := idgen.New("grev")
	if err != nil {
		return CreateGroupRevisionResult{}, err
	}
	now := s.now().UTC()
	configHash := SHA256Hex(canonical)
	requestHash := SHA256Hex(append([]byte(string(engine)+"\n"), canonical...))
	event, err := audit.NewEvent(now, "administrator", adminID, "group_revision.create", "group_revision", id, "succeeded", map[string]any{
		"group_id":      groupID,
		"engine":        engine,
		"config_sha256": configHash,
	})
	if err != nil {
		return CreateGroupRevisionResult{}, err
	}
	return s.repository.CreateGroupRevision(ctx, CreateGroupRevisionInput{
		ID:             id,
		GroupID:        groupID,
		Engine:         engine,
		Config:         canonical,
		ConfigSHA256:   configHash,
		RequestSHA256:  requestHash,
		IdempotencyKey: idempotencyKey,
		CreatedBy:      adminID,
		CreatedAt:      now,
	}, event)
}

func (s *Service) DesiredNodeConfig(ctx context.Context, nodeID string) (NodeConfigGeneration, error) {
	config, _, err := s.repository.DesiredNodeConfig(ctx, nodeID)
	return config, err
}

func (s *Service) RecordApplyResult(ctx context.Context, nodeID string, generation int64, phase agentv1.ApplyPhase, status agentv1.ApplyStatus, configSHA256, engineMode, message, attemptID string) (nodes.Node, error) {
	if generation <= 0 {
		return nodes.Node{}, fmt.Errorf("%w: generation must be positive", faults.ErrValidation)
	}
	if !validPhase(phase) {
		return nodes.Node{}, fmt.Errorf("%w: invalid phase", faults.ErrValidation)
	}
	if !validStatus(status) {
		return nodes.Node{}, fmt.Errorf("%w: invalid status", faults.ErrValidation)
	}
	if !validSHA256(configSHA256) {
		return nodes.Node{}, fmt.Errorf("%w: config_hash must be a lowercase SHA-256 value", faults.ErrValidation)
	}
	if attemptID != "" && !validAttemptID(attemptID) {
		return nodes.Node{}, fmt.Errorf("%w: apply_attempt_id must be 32 lowercase hexadecimal characters", faults.ErrValidation)
	}
	if engineMode != "" && attemptID == "" {
		return nodes.Node{}, fmt.Errorf("%w: engine_mode requires apply_attempt_id", faults.ErrValidation)
	}
	if engineMode != "" && engineMode != "gost" && engineMode != "xray" && engineMode != "mixed" {
		return nodes.Node{}, fmt.Errorf("%w: invalid engine_mode", faults.ErrValidation)
	}
	if (phase != agentv1.ApplyPhaseCommit && phase != agentv1.ApplyPhaseVerify || status != agentv1.ApplyStatusSucceeded) && engineMode != "" {
		return nodes.Node{}, fmt.Errorf("%w: engine_mode requires a successful commit or verify", faults.ErrValidation)
	}
	message = sanitizeMessage(message, 1024)
	now := s.now().UTC()
	id, err := idgen.New("ares")
	if err != nil {
		return nodes.Node{}, err
	}
	event, err := audit.NewEvent(now, "node", nodeID, "node_config.apply_result", "node_config_generation", fmt.Sprintf("%s:%d", nodeID, generation), "succeeded", map[string]any{
		"phase":  phase,
		"status": status,
	})
	if err != nil {
		return nodes.Node{}, err
	}
	node, err := s.repository.RecordApplyResult(ctx, ApplyResult{
		ID:           id,
		NodeID:       nodeID,
		Generation:   generation,
		AttemptID:    attemptID,
		Phase:        phase,
		Status:       status,
		ConfigSHA256: configSHA256,
		EngineMode:   engineMode,
		Message:      message,
		CreatedAt:    now,
	}, event)
	if err == nil && phase == agentv1.ApplyPhaseVerify && status == agentv1.ApplyStatusSucceeded && node.AppliedGeneration >= generation && s.onDeployment != nil {
		s.onDeployment(nodeID)
	}
	return node, err
}

func validAttemptID(value string) bool {
	if len(value) != 32 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPhase(phase agentv1.ApplyPhase) bool {
	switch phase {
	case agentv1.ApplyPhasePrepare, agentv1.ApplyPhaseValidate, agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify, agentv1.ApplyPhaseRollback:
		return true
	default:
		return false
	}
}

func validStatus(status agentv1.ApplyStatus) bool {
	switch status {
	case agentv1.ApplyStatusSucceeded, agentv1.ApplyStatusFailed, agentv1.ApplyStatusRolledBack:
		return true
	default:
		return false
	}
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sanitizeMessage(message string, maxRunes int) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, message)
	runes := []rune(message)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}
