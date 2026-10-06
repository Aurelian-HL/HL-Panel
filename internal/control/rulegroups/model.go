// Package rulegroups owns forwarding-rule classification and transactional
// batch operations. It deliberately stays separate from forwarding lifecycle.
package rulegroups

import (
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

type BatchOperation string

const (
	BatchPause     BatchOperation = "pause"
	BatchResume    BatchOperation = "resume"
	BatchMoveGroup BatchOperation = "move_group"
	BatchDelete    BatchOperation = "delete"
)

type RuleGroup struct {
	ID                   string    `json:"id"`
	OwnerAdministratorID string    `json:"owner_administrator_id,omitempty"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	Revision             int64     `json:"revision"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type Request struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Revision    int64  `json:"revision"`
}

type BatchRequest struct {
	Operation         BatchOperation   `json:"operation"`
	RuleIDs           []string         `json:"rule_ids"`
	RuleGroupID       string           `json:"rule_group_id"`
	ExpectedRevisions map[string]int64 `json:"expected_revisions"`
}

type BatchResult struct {
	Operation   BatchOperation    `json:"operation"`
	RuleGroupID string            `json:"rule_group_id"`
	Items       []forwarding.Rule `json:"items"`
}
