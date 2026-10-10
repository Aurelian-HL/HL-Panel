// Package gatewaymembership publishes bounded device-group candidate snapshots
// for a dedicated connection gateway credential.
package gatewaymembership

import (
	"context"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/gateway/membership"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

type State struct {
	Revision       uint64
	Pool           endpoints.EndpointPool
	Rule           forwarding.Rule
	Members        []endpoints.EndpointPoolMember
	Deployments    map[string]DeploymentEvidence
	ProtocolHealth map[string]ProtocolObservation
}

// DeploymentEvidence is evaluated from the same repository snapshot as the
// pool and rule. A receipt for an older rule revision cannot authorize a node.
type DeploymentEvidence struct {
	RuleRevision int64
	Status       deploymentreceipts.Status
}

// ProtocolObservation is a bounded, rule- and config-specific protocol probe
// result. It must be supplied by an authenticated control-plane observer, not
// by the gateway's TCP reachability monitor or a node heartbeat.
type ProtocolObservation struct {
	RuleID               string
	NodeID               string
	RuleRevision         int64
	NodeConfigGeneration int64
	ConfigSHA256         string
	Protocol             forwarding.IngressProtocol
	DialHost             string
	VerifiedAt           time.Time
	LeaseExpiresAt       time.Time
}

type Repository interface {
	GatewayMembershipState(context.Context, string) (State, error)
}

// ProtocolHealthPublisher accepts only observations produced by the protocol
// observer after a real VLESS challenge. Heartbeat/TCP monitors must not call
// this interface.
type ProtocolHealthPublisher interface {
	PublishProtocolHealth(context.Context, ProtocolObservation) error
}

type Service struct {
	repository Repository
	clock      func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, clock: time.Now}
}

// ReadyCandidateCount uses the same authorized view published to the gateway.
// It must not count heartbeat-only or TCP-reachable members as healthy.
func (s *Service) ReadyCandidateCount(ctx context.Context, poolID string) (int, error) {
	snapshot, err := s.Snapshot(ctx, poolID)
	if err != nil {
		return 0, err
	}
	now := s.clock().UTC()
	count := 0
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.Status == endpointrouter.EndpointReady && endpoint.HealthLeaseExpiresAt.After(now) {
			count++
		}
	}
	return count, nil
}

// Snapshot publishes deployment- and protocol-bound membership. A ready
// endpoint is selectable only before its original health lease expires.
// Expiry is enforced by the router and ReadyCandidateCount, without changing
// the snapshot's content at an unchanged repository revision.
func (s *Service) Snapshot(ctx context.Context, poolID string) (membership.Snapshot, error) {
	state, err := s.repository.GatewayMembershipState(ctx, poolID)
	if err != nil {
		return membership.Snapshot{}, err
	}
	items := make([]endpointrouter.Endpoint, 0, len(state.Members))
	rule := state.Rule
	if poolAuthorizesRule(state.Pool, rule) {
		now := s.clock().UTC()
		for _, member := range state.Members {
			if member.PoolID != state.Pool.ID || member.GroupID != state.Pool.GroupID ||
				member.State != endpoints.CandidateEligible || member.Weight < 1 || member.ActiveConnections < 0 {
				continue
			}
			host, err := serviceaddress.NormalizeHost(member.DialHost)
			if err != nil || host == "" {
				continue
			}
			status := endpointrouter.EndpointOffline
			leaseExpiresAt := time.Unix(0, 0).UTC()
			deployment := state.Deployments[member.NodeID]
			if deploymentReady(deployment, rule, member.NodeID) && protocolEvidenceMatches(state.ProtocolHealth[member.NodeID], deployment.Status, rule, member, host, now) {
				status = endpointrouter.EndpointReady
				leaseExpiresAt = state.ProtocolHealth[member.NodeID].LeaseExpiresAt
			}
			items = append(items, endpointrouter.Endpoint{
				ID: member.NodeID, Address: net.JoinHostPort(host, strconv.Itoa(rule.ListenPort)),
				Weight: member.Weight, Status: status,
				HealthLeaseExpiresAt: leaseExpiresAt,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return membership.Snapshot{Revision: state.Revision, Endpoints: items}, nil
}

func poolAuthorizesRule(pool endpoints.EndpointPool, rule forwarding.Rule) bool {
	return pool.Mode.Valid() && pool.RuleID != "" && rule.ID == pool.RuleID &&
		rule.Revision > 0 && rule.EntryGroupID == pool.GroupID &&
		rule.ListenPort == pool.Port && rule.Protocol == forwarding.ProtocolTCP &&
		pool.Protocol == "vless" && rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality &&
		!rule.Paused && (rule.Status == forwarding.StatusPendingActivation || rule.Status == forwarding.StatusActive) && rule.ActivationReason == "" &&
		rule.IngressReadiness() == forwarding.IngressReady
}

// MatchesCurrentDeployment must be checked again when publishing a probe:
// its rule, address or node configuration may have changed during the challenge.
// A newly published observation must still be live, even though an existing
// snapshot retains expired leases for stable revision content.
func (o ProtocolObservation) MatchesCurrentDeployment(pool endpoints.EndpointPool, rule forwarding.Rule, member endpoints.EndpointPoolMember, evidence DeploymentEvidence, now time.Time) bool {
	if !poolAuthorizesRule(pool, rule) || member.PoolID != pool.ID || member.GroupID != pool.GroupID ||
		member.State != endpoints.CandidateEligible || member.Weight < 1 || member.ActiveConnections < 0 ||
		!deploymentReady(evidence, rule, member.NodeID) {
		return false
	}
	host, err := serviceaddress.NormalizeHost(member.DialHost)
	return err == nil && host != "" && protocolHealthy(o, evidence.Status, rule, member, host, now)
}

// CanReplace orders observations only within the same deployment and address.
// An identical retry is safe, but delayed proof must not roll back a newer lease.
// A different deployment can supersede old evidence after current-state checks.
func (o ProtocolObservation) CanReplace(previous ProtocolObservation) bool {
	previousHost, previousErr := serviceaddress.NormalizeHost(previous.DialHost)
	host, err := serviceaddress.NormalizeHost(o.DialHost)
	if o.RuleID != previous.RuleID || o.NodeID != previous.NodeID || o.RuleRevision != previous.RuleRevision ||
		o.NodeConfigGeneration != previous.NodeConfigGeneration || o.ConfigSHA256 != previous.ConfigSHA256 ||
		o.Protocol != previous.Protocol || err != nil || previousErr != nil || host != previousHost {
		return true
	}
	return !o.VerifiedAt.Before(previous.VerifiedAt) && !o.LeaseExpiresAt.Before(previous.LeaseExpiresAt) &&
		(!o.VerifiedAt.Equal(previous.VerifiedAt) || o.LeaseExpiresAt.Equal(previous.LeaseExpiresAt))
}

func deploymentReady(evidence DeploymentEvidence, rule forwarding.Rule, nodeID string) bool {
	return evidence.RuleRevision == rule.Revision && evidence.Status.RuleID == rule.ID &&
		evidence.Status.NodeID == nodeID && evidence.Status.ReceiptVerified &&
		evidence.Status.Reason == deploymentreceipts.ReasonReceiptVerified &&
		evidence.Status.Engine == agentv1.EngineXray && evidence.Status.NodeConfigGeneration > 0 &&
		len(evidence.Status.ConfigSHA256) == 64
}

func protocolHealthy(observation ProtocolObservation, receipt deploymentreceipts.Status, rule forwarding.Rule, member endpoints.EndpointPoolMember, dialHost string, now time.Time) bool {
	return protocolEvidenceMatches(observation, receipt, rule, member, dialHost, now) && observation.LeaseExpiresAt.After(now)
}

func protocolEvidenceMatches(observation ProtocolObservation, receipt deploymentreceipts.Status, rule forwarding.Rule, member endpoints.EndpointPoolMember, dialHost string, now time.Time) bool {
	if observation.RuleID != rule.ID || observation.NodeID != member.NodeID ||
		observation.RuleRevision != rule.Revision || observation.Protocol != forwarding.IngressVLESSReality ||
		observation.NodeConfigGeneration != receipt.NodeConfigGeneration || observation.ConfigSHA256 != receipt.ConfigSHA256 ||
		observation.VerifiedAt.IsZero() ||
		observation.VerifiedAt.After(now) || !observation.LeaseExpiresAt.After(observation.VerifiedAt) ||
		observation.LeaseExpiresAt.After(observation.VerifiedAt.Add(endpoints.DefaultHealthTTL)) {
		return false
	}
	observedHost, err := serviceaddress.NormalizeHost(observation.DialHost)
	return err == nil && observedHost == dialHost
}
