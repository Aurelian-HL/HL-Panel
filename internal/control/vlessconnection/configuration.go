package vlessconnection

import (
	"context"
	"strings"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

// Configuration is a saved client configuration, not evidence of a live service.
// Its URI contains the client's credential and belongs only in an explicitly
// authorized, non-cacheable administrator response, never in a list or a log.
type Configuration struct {
	URI                  string            `json:"uri"`
	Name                 string            `json:"name"`
	Endpoint             string            `json:"endpoint"`
	RuleID               string            `json:"rule_id"`
	CustomerID           string            `json:"customer_id"`
	BindingID            string            `json:"binding_id"`
	EndpointPoolID       string            `json:"endpoint_pool_id"`
	RuleRevision         int64             `json:"rule_revision"`
	BindingRevision      int64             `json:"binding_revision"`
	Paused               bool              `json:"paused"`
	Status               string            `json:"status"`
	RuleStatus           forwarding.Status `json:"rule_status"`
	ServiceReady         bool              `json:"service_ready"`
	LiveReadinessChecked bool              `json:"live_readiness_checked"`
}

// ConfigurationSource must authorize and validate the rule, its unique active
// binding and its endpoint in one authoritative read. It must not activate a
// rule, provision an identity or relax either existing live connection route.
type ConfigurationSource interface {
	ResolveForwardingConfiguration(context.Context, string, string) (Configuration, error)
}

func (s *Service) ResolveForwardingConfiguration(ctx context.Context, adminID, ruleID string) (Configuration, error) {
	adminID, ruleID = strings.TrimSpace(adminID), strings.TrimSpace(ruleID)
	if adminID == "" || ruleID == "" {
		return Configuration{}, faults.ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return Configuration{}, err
	}
	if s != nil {
		if source, ok := s.batch.(ConfigurationSource); ok {
			return source.ResolveForwardingConfiguration(ctx, adminID, ruleID)
		}
	}
	// Do not fall back to separate credential/rule/pool transactions: a writer
	// could change ownership or rotate an identity between those reads.
	return Configuration{}, faults.ErrConflict
}
