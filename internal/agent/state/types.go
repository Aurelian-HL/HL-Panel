package state

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const (
	MaxConfigurationBytes = 4 << 20
	MaxApplyMessageBytes  = 512
)

var (
	ErrAlreadyEnrolled     = errors.New("agent is already enrolled")
	ErrCredentialsNotFound = errors.New("agent credentials not found")
	ErrGenerationRollback  = errors.New("node configuration generation rollback rejected")
	ErrGenerationConflict  = errors.New("node configuration generation hash conflict")
	ErrNoLastKnownGood     = errors.New("last-known-good configuration not found")
	ErrNoPendingApply      = errors.New("pending apply not found")
	ErrPendingApplyExists  = errors.New("a different configuration apply is already pending")
)

type Credentials struct {
	NodeID         string `json:"node_id"`
	NodeCredential string `json:"node_credential"`
	EnrolledAt     string `json:"enrolled_at"`
}

func (c Credentials) validate() error {
	if err := validateCredentialField(c.NodeID, "node_id"); err != nil {
		return err
	}
	if err := validateCredentialField(c.NodeCredential, "node_credential"); err != nil {
		return err
	}
	return nil
}

func validateCredentialField(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("credential file has an empty %s", name)
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("credential file has invalid whitespace in %s", name)
	}
	if len(value) > 4096 {
		return fmt.Errorf("credential file %s exceeds size limit", name)
	}
	return nil
}

type AppliedNodeConfig struct {
	Generation   agentv1.NodeConfigGeneration `json:"generation"`
	Engine       agentv1.Engine               `json:"engine"`
	ConfigSHA256 string                       `json:"config_sha256"`
	AppliedAt    string                       `json:"applied_at"`
}

type PendingNodeConfig struct {
	Generation   agentv1.NodeConfigGeneration `json:"generation"`
	Engine       agentv1.Engine               `json:"engine"`
	ConfigSHA256 string                       `json:"config_sha256"`
	StartedAt    string                       `json:"started_at"`
}

type AgentState struct {
	Applied          *AppliedNodeConfig           `json:"applied,omitempty"`
	Pending          *PendingNodeConfig           `json:"pending,omitempty"`
	PendingReceipts  []agentv1.ApplyResultRequest `json:"pending_receipts,omitempty"`
	LastApplyPhase   agentv1.ApplyPhase           `json:"last_apply_phase,omitempty"`
	LastApplyStatus  agentv1.ApplyStatus          `json:"last_apply_status,omitempty"`
	LastApplyMessage string                       `json:"last_apply_message,omitempty"`
	UpdatedAt        string                       `json:"updated_at,omitempty"`
}

func (s AgentState) AppliedGeneration() agentv1.NodeConfigGeneration {
	if s.Applied == nil {
		return 0
	}
	return s.Applied.Generation
}

func (s AgentState) validate() error {
	if s.Applied != nil {
		if err := validateReference(s.Applied.Generation, s.Applied.Engine, s.Applied.ConfigSHA256); err != nil {
			return fmt.Errorf("invalid applied configuration: %w", err)
		}
	}
	if s.Pending != nil {
		if err := validateReference(s.Pending.Generation, s.Pending.Engine, s.Pending.ConfigSHA256); err != nil {
			return fmt.Errorf("invalid pending configuration: %w", err)
		}
		if s.Applied != nil && s.Pending.Generation <= s.Applied.Generation {
			return errors.New("pending generation is not newer than applied generation")
		}
	}
	if len([]byte(s.LastApplyMessage)) > MaxApplyMessageBytes {
		return errors.New("last apply message exceeds size limit")
	}
	if err := validatePendingReceipts(s.PendingReceipts); err != nil {
		return err
	}
	if len(s.PendingReceipts) > 0 && (s.Applied == nil || s.PendingReceipts[0].Generation != s.Applied.Generation ||
		s.PendingReceipts[0].ConfigSHA256 != s.Applied.ConfigSHA256 || s.Pending != nil) {
		return errors.New("pending apply receipts do not match applied configuration")
	}
	return nil
}

func validateReference(generation agentv1.NodeConfigGeneration, engine agentv1.Engine, hash string) error {
	if generation == 0 {
		return errors.New("generation must be greater than zero")
	}
	if engine != agentv1.EngineNodeBundle {
		return fmt.Errorf("unsupported engine %q", engine)
	}
	if _, err := normalizeSHA256(hash); err != nil {
		return err
	}
	return nil
}

func sanitizeMessage(message string) string {
	message = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) <= MaxApplyMessageBytes {
		return message
	}
	message = message[:MaxApplyMessageBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
