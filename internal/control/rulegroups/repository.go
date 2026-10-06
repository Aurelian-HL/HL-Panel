package rulegroups

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type SaveInput struct {
	RuleGroup        RuleGroup
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	CreatedBy        string
}

type BatchInput struct {
	Request            BatchRequest
	ExpectedCustomerID string
	UpdatedAt          time.Time
	IdempotencyKey     string
	RequestSHA256      string
	CreatedBy          string
}

// Repository mutations persist their models, audit event and idempotency
// result atomically. BatchUpdateRules is all-or-nothing across every rule.
type Repository interface {
	ListRuleGroups(context.Context) ([]RuleGroup, error)
	RuleGroup(context.Context, string) (RuleGroup, error)
	ListRuleGroupsForAdministrator(context.Context, string) ([]RuleGroup, error)
	RuleGroupForAdministrator(context.Context, string, string) (RuleGroup, error)
	SaveRuleGroup(context.Context, SaveInput, audit.Event) (RuleGroup, bool, error)
	BatchUpdateRules(context.Context, BatchInput, audit.Event) (BatchResult, bool, error)
}
