package rulegroups

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func NormalizeRequest(input Request) (Request, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 128 || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 {
		return Request{}, fmt.Errorf("%w: name must contain 1 to 128 printable characters", faults.ErrValidation)
	}
	if utf8.RuneCountInString(input.Description) > 1024 || strings.IndexFunc(input.Description, unicode.IsControl) >= 0 {
		return Request{}, fmt.Errorf("%w: description must contain at most 1024 printable characters", faults.ErrValidation)
	}
	if input.Revision < 0 {
		return Request{}, fmt.Errorf("%w: revision must not be negative", faults.ErrValidation)
	}
	return input, nil
}

func NormalizeBatchRequest(input BatchRequest) (BatchRequest, error) {
	input.RuleGroupID = strings.TrimSpace(input.RuleGroupID)
	if input.Operation != BatchPause && input.Operation != BatchResume && input.Operation != BatchMoveGroup && input.Operation != BatchDelete {
		return BatchRequest{}, fmt.Errorf("%w: operation must be pause, resume, move_group or delete", faults.ErrValidation)
	}
	if input.Operation != BatchMoveGroup && input.RuleGroupID != "" {
		return BatchRequest{}, fmt.Errorf("%w: rule_group_id is only valid for move_group", faults.ErrValidation)
	}
	if input.RuleGroupID != "" && !validReference(input.RuleGroupID) {
		return BatchRequest{}, fmt.Errorf("%w: invalid rule_group_id", faults.ErrValidation)
	}
	if len(input.RuleIDs) < 1 || len(input.RuleIDs) > 500 || len(input.ExpectedRevisions) != len(input.RuleIDs) {
		return BatchRequest{}, fmt.Errorf("%w: provide 1 to 500 rules and one expected revision per rule", faults.ErrValidation)
	}
	seen := make(map[string]struct{}, len(input.RuleIDs))
	ids := make([]string, len(input.RuleIDs))
	for index, rawID := range input.RuleIDs {
		id := strings.TrimSpace(rawID)
		revision, exists := input.ExpectedRevisions[id]
		if !validReference(id) || !exists || revision < 1 {
			return BatchRequest{}, fmt.Errorf("%w: every rule requires a valid id and positive expected revision", faults.ErrValidation)
		}
		if _, exists := seen[id]; exists {
			return BatchRequest{}, fmt.Errorf("%w: duplicate rule id", faults.ErrValidation)
		}
		seen[id] = struct{}{}
		ids[index] = id
	}
	input.RuleIDs = ids
	return input, nil
}

func validReference(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
