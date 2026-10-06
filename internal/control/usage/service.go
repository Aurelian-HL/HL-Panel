package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const maxPageSize = 200

type Service struct {
	repository         Repository
	policies           CustomerPolicyProvider
	limits             LimitPolicyProvider
	legacyRuleMetadata LegacyRuleMetadataProvider
	now                func() time.Time
}

type Option func(*Service)

func WithLimitPolicyProvider(provider LimitPolicyProvider) Option {
	return func(service *Service) { service.limits = provider }
}

func WithLegacyRuleMetadataProvider(provider LegacyRuleMetadataProvider) Option {
	return func(service *Service) { service.legacyRuleMetadata = provider }
}

func NewService(repository Repository, policies CustomerPolicyProvider, now func() time.Time, options ...Option) *Service {
	if now == nil {
		now = time.Now
	}
	service := &Service{repository: repository, policies: policies, now: now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (service *Service) Ingest(ctx context.Context, input Report) (IngestResult, error) {
	if strings.TrimSpace(input.LegacyRuleID) != "" {
		if repository, ok := service.repository.(LegacyReplayRepository); ok {
			result, replayed, err := repository.ReplayLegacy(ctx, input)
			if err != nil {
				return IngestResult{}, err
			}
			if replayed {
				return result, nil
			}
		}
	}
	if err := service.resolveLegacyRuleMetadata(ctx, &input); err != nil {
		return IngestResult{}, err
	}
	normalized, err := normalizeReport(input)
	if err != nil {
		return IngestResult{}, err
	}
	if service.repository == nil || service.policies == nil {
		return IngestResult{}, errorsUnavailable()
	}
	charged, err := chargedBytes(normalized.CustomerActualBytes, normalized.EntryMultiplierMicros, normalized.ExitMultiplierMicros)
	if err != nil {
		return IngestResult{}, err
	}
	payloadHash, err := reportFingerprint(normalized)
	if err != nil {
		return IngestResult{}, err
	}
	now := service.now().UTC()
	event := Event{Report: normalized, ChargedBytes: charged, PayloadSHA256: payloadHash, ReceivedAt: now}
	return service.repository.Ingest(ctx, event, func(totals CustomerTotals) (*EnforcementDecision, error) {
		policy, err := service.policies.UsagePolicy(ctx, normalized.CustomerID)
		if err != nil {
			return nil, err
		}
		if policy.CustomerID != normalized.CustomerID || policy.TrafficLimitBytes < 0 {
			return nil, fmt.Errorf("%w: invalid customer usage policy", faults.ErrValidation)
		}
		return projectEnforcement(policy, totals, event, now), nil
	})
}

// LegacyReplayMatches compares every field available from an old-format
// report against the persisted normalized event. Rule ownership fields are
// intentionally taken from the event, never from the agent report.
func LegacyReplayMatches(event Event, report Report) bool {
	ruleID := strings.TrimSpace(report.LegacyRuleID)
	if !validIdentifier(ruleID) || (strings.TrimSpace(report.RuleID) != "" && strings.TrimSpace(report.RuleID) != ruleID) ||
		strings.TrimSpace(report.CustomerID) != "" || strings.TrimSpace(report.EntryGroupID) != "" ||
		strings.TrimSpace(report.ExitGroupID) != "" || strings.TrimSpace(report.Protocol) != "" ||
		report.RuleActualBytes < 0 {
		return false
	}
	if event.NodeID != strings.TrimSpace(report.NodeID) || event.BootID != strings.TrimSpace(report.BootID) ||
		event.Sequence != report.Sequence || event.RuleID != ruleID ||
		(event.Protocol != "tcp" && event.Protocol != "udp") ||
		event.RuleActualBytes != report.RuleActualBytes || event.CustomerActualBytes != report.RuleActualBytes ||
		!samePostgresTimestamp(event.OccurredAt, report.OccurredAt) ||
		!samePostgresTimestamp(event.PeriodStartedAt, report.PeriodStartedAt) ||
		!samePostgresTimestamp(event.PeriodEndedAt, report.PeriodEndedAt) {
		return false
	}

	// PostgreSQL stores timestamps at microsecond precision. The durable source
	// fingerprint retains the original precision, so compare it after rebuilding
	// the normalized legacy report with ownership fields from the stored event.
	normalized, err := normalizeReport(Report{
		NodeID: report.NodeID, BootID: report.BootID, Sequence: report.Sequence,
		CustomerID: event.CustomerID, RuleID: event.RuleID,
		EntryGroupID: event.EntryGroupID, ExitGroupID: event.ExitGroupID, Protocol: event.Protocol,
		OccurredAt: report.OccurredAt, PeriodStartedAt: report.PeriodStartedAt, PeriodEndedAt: report.PeriodEndedAt,
		RuleActualBytes: report.RuleActualBytes, CustomerActualBytes: report.RuleActualBytes,
		EntryMultiplierMicros: event.EntryMultiplierMicros, ExitMultiplierMicros: event.ExitMultiplierMicros,
	})
	if err != nil {
		return false
	}
	fingerprint, err := reportFingerprint(normalized)
	return err == nil && event.PayloadSHA256 != "" && event.PayloadSHA256 == fingerprint
}

func samePostgresTimestamp(left, right time.Time) bool {
	return left.Truncate(time.Microsecond).Equal(right.Truncate(time.Microsecond))
}

func (service *Service) resolveLegacyRuleMetadata(ctx context.Context, report *Report) error {
	if report == nil || strings.TrimSpace(report.LegacyRuleID) == "" {
		return nil
	}
	ruleID := strings.TrimSpace(report.LegacyRuleID)
	if !validIdentifier(ruleID) || (strings.TrimSpace(report.RuleID) != "" && strings.TrimSpace(report.RuleID) != ruleID) ||
		strings.TrimSpace(report.CustomerID) != "" || strings.TrimSpace(report.EntryGroupID) != "" ||
		strings.TrimSpace(report.ExitGroupID) != "" || strings.TrimSpace(report.Protocol) != "" {
		return fmt.Errorf("%w: legacy usage report must not provide rule metadata", faults.ErrValidation)
	}
	if service.legacyRuleMetadata == nil {
		return errorsUnavailable()
	}
	metadata, err := service.legacyRuleMetadata.ResolveLegacyRuleUsageMetadata(ctx, report.NodeID, ruleID)
	if err != nil {
		return err
	}
	if metadata.RuleID != ruleID || !validIdentifier(metadata.CustomerID) || !validIdentifier(metadata.EntryGroup) ||
		(metadata.ExitGroup != "" && !validIdentifier(metadata.ExitGroup)) || (metadata.Protocol != "tcp" && metadata.Protocol != "udp") {
		return fmt.Errorf("%w: legacy rule metadata is invalid", faults.ErrValidation)
	}
	if report.RuleActualBytes < 0 {
		return ErrNegativeBytes
	}
	report.RuleID = metadata.RuleID
	report.CustomerID = metadata.CustomerID
	report.EntryGroupID = metadata.EntryGroup
	report.ExitGroupID = metadata.ExitGroup
	report.Protocol = metadata.Protocol
	report.CustomerActualBytes = report.RuleActualBytes
	report.EntryMultiplierMicros = metadata.EntryMultiplierMicros
	report.ExitMultiplierMicros = metadata.ExitMultiplierMicros
	report.LegacyRuleID = ""
	return nil
}

func (service *Service) Query(ctx context.Context, query Query) (QueryResult, error) {
	if service.repository == nil {
		return QueryResult{}, errorsUnavailable()
	}
	normalized, err := normalizeQuery(query)
	if err != nil {
		return QueryResult{}, err
	}
	result, err := service.repository.Query(ctx, normalized)
	if err != nil {
		return QueryResult{}, err
	}
	if result.Items == nil {
		result.Items = []Record{}
	}
	result.Page, result.PageSize = normalized.Page, normalized.PageSize
	return result, nil
}

func (service *Service) DesiredEnforcement(ctx context.Context, nodeID string) (*agentv1.EnforcementCommand, error) {
	if service.repository == nil {
		return nil, errorsUnavailable()
	}
	nodeID = strings.TrimSpace(nodeID)
	if !validIdentifier(nodeID) {
		return nil, fmt.Errorf("%w: invalid node id", faults.ErrValidation)
	}
	decision, err := service.repository.DesiredEnforcement(ctx, nodeID)
	if err != nil || decision == nil {
		return nil, err
	}
	if decision.TriggerNodeID != nodeID || (decision.Status != EnforcementPending && decision.Status != EnforcementRevokePending) {
		return nil, errors.New("repository returned an invalid enforcement command")
	}
	limits := agentv1.EffectiveLimits{}
	protocol := decision.Protocol
	if service.limits != nil {
		limits, protocol, err = service.limits.EffectiveLimits(ctx, decision.CustomerID, decision.RuleID)
		if err != nil {
			return nil, err
		}
	}
	if protocol == "udp" {
		limits = agentv1.EffectiveLimits{}
	}
	if protocol != "tcp" && protocol != "udp" {
		return nil, fmt.Errorf("%w: enforcement rule protocol is invalid", faults.ErrValidation)
	}
	return &agentv1.EnforcementCommand{DecisionID: decision.ID, CustomerID: decision.CustomerID,
		RuleID: decision.RuleID, Protocol: protocol, Action: decision.Action, Revision: decision.Revision,
		Limits: limits}, nil
}

func (service *Service) RecordEnforcementResult(ctx context.Context, nodeID string, input agentv1.EnforcementResultRequest) (EnforcementDecision, bool, error) {
	if service.repository == nil {
		return EnforcementDecision{}, false, errorsUnavailable()
	}
	nodeID = strings.TrimSpace(nodeID)
	input.DecisionID = strings.TrimSpace(input.DecisionID)
	input.Message = sanitizeEnforcementMessage(input.Message)
	if !validIdentifier(nodeID) || !validIdentifier(input.DecisionID) || input.Revision <= 0 {
		return EnforcementDecision{}, false, fmt.Errorf("%w: invalid enforcement result identity or revision", faults.ErrValidation)
	}
	if input.Action != agentv1.EnforcementDisableCustomer && input.Action != agentv1.EnforcementEnableCustomer {
		return EnforcementDecision{}, false, fmt.Errorf("%w: invalid enforcement action", faults.ErrValidation)
	}
	if input.Status != agentv1.EnforcementResultSucceeded && input.Status != agentv1.EnforcementResultFailed {
		return EnforcementDecision{}, false, fmt.Errorf("%w: invalid enforcement result status", faults.ErrValidation)
	}
	payload, err := json.Marshal(struct {
		NodeID string
		Input  agentv1.EnforcementResultRequest
	}{nodeID, input})
	if err != nil {
		return EnforcementDecision{}, false, err
	}
	digest := sha256.Sum256(payload)
	return service.repository.RecordEnforcementResult(ctx, EnforcementResult{
		DecisionID: input.DecisionID, NodeID: nodeID, Action: input.Action, Revision: input.Revision,
		Status: input.Status, Message: input.Message, PayloadSHA256: hex.EncodeToString(digest[:]), CreatedAt: service.now().UTC(),
	})
}

func (service *Service) RequestRevoke(ctx context.Context, administratorID, decisionID, idempotencyKey string) (EnforcementDecision, bool, error) {
	if service.repository == nil {
		return EnforcementDecision{}, false, errorsUnavailable()
	}
	administratorID = strings.TrimSpace(administratorID)
	decisionID = strings.TrimSpace(decisionID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validIdentifier(administratorID) || !validIdentifier(decisionID) || !validIdentifier(idempotencyKey) {
		return EnforcementDecision{}, false, fmt.Errorf("%w: invalid administrator, decision or Idempotency-Key", faults.ErrValidation)
	}
	raw, err := json.Marshal(struct{ AdministratorID, DecisionID string }{administratorID, decisionID})
	if err != nil {
		return EnforcementDecision{}, false, err
	}
	digest := sha256.Sum256(raw)
	return service.repository.RequestRevoke(ctx, RevokeRequest{
		DecisionID: decisionID, AdministratorID: administratorID, IdempotencyKey: idempotencyKey,
		RequestSHA256: hex.EncodeToString(digest[:]), RequestedAt: service.now().UTC(),
	})
}

func normalizeReport(input Report) (Report, error) {
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.BootID = strings.TrimSpace(input.BootID)
	input.CustomerID = strings.TrimSpace(input.CustomerID)
	input.RuleID = strings.TrimSpace(input.RuleID)
	input.EntryGroupID = strings.TrimSpace(input.EntryGroupID)
	input.ExitGroupID = strings.TrimSpace(input.ExitGroupID)
	input.Protocol = strings.ToLower(strings.TrimSpace(input.Protocol))
	for name, value := range map[string]string{
		"node_id": input.NodeID, "boot_id": input.BootID, "customer_id": input.CustomerID,
		"rule_id": input.RuleID, "entry_group_id": input.EntryGroupID,
	} {
		if !validIdentifier(value) {
			return Report{}, fmt.Errorf("%w: %s must contain 1 to 128 non-space characters", faults.ErrValidation, name)
		}
	}
	if input.ExitGroupID != "" && !validIdentifier(input.ExitGroupID) {
		return Report{}, fmt.Errorf("%w: exit_group_id must be empty or a valid identifier", faults.ErrValidation)
	}
	if input.Protocol != "tcp" && input.Protocol != "udp" {
		return Report{}, fmt.Errorf("%w: protocol must be tcp or udp", faults.ErrValidation)
	}
	if input.Sequence <= 0 {
		return Report{}, fmt.Errorf("%w: sequence must be positive", faults.ErrValidation)
	}
	if input.RuleActualBytes < 0 || input.CustomerActualBytes < 0 {
		return Report{}, ErrNegativeBytes
	}
	if !validMultiplier(input.EntryMultiplierMicros) || !validMultiplier(input.ExitMultiplierMicros) {
		return Report{}, ErrInvalidMultiplier
	}
	if input.OccurredAt.IsZero() || input.PeriodStartedAt.IsZero() || input.PeriodEndedAt.IsZero() {
		return Report{}, fmt.Errorf("%w: usage timestamps are required", faults.ErrValidation)
	}
	input.OccurredAt = input.OccurredAt.UTC()
	input.PeriodStartedAt = input.PeriodStartedAt.UTC()
	input.PeriodEndedAt = input.PeriodEndedAt.UTC()
	if !input.PeriodEndedAt.After(input.PeriodStartedAt) {
		return Report{}, fmt.Errorf("%w: period_ended_at must be after period_started_at", faults.ErrValidation)
	}
	return input, nil
}

func normalizeQuery(input Query) (Query, error) {
	if input.Scope == "" {
		input.Scope = ScopeSite
	}
	if !input.Scope.Valid() {
		return Query{}, fmt.Errorf("%w: scope must be site, customer, rule or device_group", faults.ErrValidation)
	}
	input.ScopeID = strings.TrimSpace(input.ScopeID)
	if input.Scope == ScopeSite {
		if input.ScopeID != "" {
			return Query{}, fmt.Errorf("%w: scope_id must be empty for site scope", faults.ErrValidation)
		}
	} else if !validIdentifier(input.ScopeID) {
		return Query{}, fmt.Errorf("%w: scope_id is required for the selected scope", faults.ErrValidation)
	}
	if input.Page == 0 {
		input.Page = 1
	}
	if input.PageSize == 0 {
		input.PageSize = 50
	}
	if input.Page < 1 || input.PageSize < 1 || input.PageSize > maxPageSize || input.Page > math.MaxInt/input.PageSize {
		return Query{}, fmt.Errorf("%w: page must be positive and page_size must be between 1 and %d", faults.ErrValidation, maxPageSize)
	}
	if input.From != nil {
		value := input.From.UTC()
		input.From = &value
	}
	if input.To != nil {
		value := input.To.UTC()
		input.To = &value
	}
	if input.From != nil && input.To != nil && !input.To.After(*input.From) {
		return Query{}, fmt.Errorf("%w: to must be after from", faults.ErrValidation)
	}
	return input, nil
}

// chargedBytes rounds each event upward after applying both fixed-point
// multiplier snapshots. This prevents systematically losing sub-byte fractions
// and makes replay independent of later multiplier changes.
func chargedBytes(actual, entryMicros, exitMicros int64) (int64, error) {
	if actual < 0 {
		return 0, ErrNegativeBytes
	}
	if !validMultiplier(entryMicros) || !validMultiplier(exitMicros) {
		return 0, ErrInvalidMultiplier
	}
	numerator := new(big.Int).SetInt64(actual)
	numerator.Mul(numerator, big.NewInt(entryMicros))
	numerator.Mul(numerator, big.NewInt(exitMicros))
	denominator := big.NewInt(MultiplierScale * MultiplierScale)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, ErrByteOverflow
	}
	return quotient.Int64(), nil
}

func validMultiplier(value int64) bool {
	return value >= 0 && value <= MaxMultiplierMicros
}

func reportFingerprint(report Report) (string, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func projectEnforcement(policy CustomerPolicy, totals CustomerTotals, event Event, now time.Time) *EnforcementDecision {
	reason := EnforcementReason("")
	switch {
	case policy.Disabled:
		reason = EnforcementCustomerDisabled
	case policy.ExpiresAt != nil && !policy.ExpiresAt.After(now):
		reason = EnforcementCustomerExpired
	case policy.TrafficLimitBytes > 0 && totals.ChargedBytes >= policy.TrafficLimitBytes:
		reason = EnforcementQuotaExhausted
	default:
		return nil
	}
	identity := fmt.Sprintf("%s\x00%s\x00%d\x00%s", event.NodeID, event.BootID, event.Sequence, reason)
	digest := sha256.Sum256([]byte(identity))
	return &EnforcementDecision{
		ID: "ued_" + hex.EncodeToString(digest[:16]), CustomerID: event.CustomerID, RuleID: event.RuleID,
		Protocol: event.Protocol, Reason: reason,
		Action: agentv1.EnforcementDisableCustomer, Status: EnforcementPending, TriggerNodeID: event.NodeID,
		TriggerBootID: event.BootID, TriggerSequence: event.Sequence, CustomerChargedBytes: totals.ChargedBytes,
		TrafficLimitBytes: policy.TrafficLimitBytes, CreatedAt: now, UpdatedAt: now, Revision: 1,
	}
}

func sanitizeEnforcementMessage(message string) string {
	message = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) <= 512 {
		return message
	}
	message = message[:512]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

func errorsUnavailable() error {
	return fmt.Errorf("usage service dependencies are unavailable")
}
