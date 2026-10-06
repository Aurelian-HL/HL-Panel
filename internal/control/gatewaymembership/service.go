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
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/gateway/membership"
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

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

// ReadyCandidateCount uses the same authorized view published to the gateway.
// It must not count heartbeat-only or TCP-reachable members as healthy.
func (s *Service) ReadyCandidateCount(ctx context.Context, poolID string) (int, error) {
	snapshot, err := s.Snapshot(ctx, poolID)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	count := 0
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.Status == endpointrouter.EndpointReady && endpoint.HealthLeaseExpiresAt.After(now) {
			count++
		}
	}
	return count, nil
}

// Snapshot emits ready members only when current real-engine deployment and
// independent, bounded protocol health refer to the same rule and node config.
// The current repository does not yet ingest protocol observations, so live
// members remain offline until that authenticated integration is supplied.
func (s *Service) Snapshot(ctx context.Context, poolID string) (membership.Snapshot, error) {
	state, err := s.repository.GatewayMembershipState(ctx, poolID)
	if err != nil {
		return membership.Snapshot{}, err
	}
	items := make([]endpointrouter.Endpoint, 0, len(state.Members))
	rule := state.Rule
	if state.Pool.Mode.Valid() && state.Pool.RuleID != "" && rule.ID == state.Pool.RuleID &&
		rule.Revision > 0 && rule.EntryGroupID == state.Pool.GroupID &&
		rule.ListenPort == state.Pool.Port && rule.Protocol == forwarding.ProtocolTCP &&
		state.Pool.Protocol == "vless" && rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality &&
		!rule.Paused && (rule.Status == forwarding.StatusPendingActivation || rule.Status == forwarding.StatusActive) && rule.ActivationReason == "" &&
		rule.IngressReadiness() == forwarding.IngressReady {
		now := time.Now().UTC()
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
			leaseExpiresAt := now
			deployment := state.Deployments[member.NodeID]
			if deploymentReady(deployment, rule, member.NodeID) && protocolHealthy(state.ProtocolHealth[member.NodeID], deployment.Status, rule, member, host, now) {
				status = endpointrouter.EndpointReady
				if observedExpiry := state.ProtocolHealth[member.NodeID].LeaseExpiresAt; observedExpiry.Before(leaseExpiresAt) {
					leaseExpiresAt = observedExpiry
				}
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

func deploymentReady(evidence DeploymentEvidence, rule forwarding.Rule, nodeID string) bool {
	return evidence.RuleRevision == rule.Revision && evidence.Status.RuleID == rule.ID &&
		evidence.Status.NodeID == nodeID && evidence.Status.ReceiptVerified &&
		evidence.Status.Reason == deploymentreceipts.ReasonReceiptVerified &&
		evidence.Status.Engine == agentv1.EngineXray && evidence.Status.NodeConfigGeneration > 0 &&
		len(evidence.Status.ConfigSHA256) == 64
}

func protocolHealthy(observation ProtocolObservation, receipt deploymentreceipts.Status, rule forwarding.Rule, member endpoints.EndpointPoolMember, dialHost string, now time.Time) bool {
	if observation.RuleID != rule.ID || observation.NodeID != member.NodeID ||
		observation.RuleRevision != rule.Revision || observation.Protocol != forwarding.IngressVLESSReality ||
		observation.NodeConfigGeneration != receipt.NodeConfigGeneration || observation.ConfigSHA256 != receipt.ConfigSHA256 ||
		observation.VerifiedAt.IsZero() ||
		observation.VerifiedAt.After(now) || !observation.LeaseExpiresAt.After(now) ||
		observation.LeaseExpiresAt.After(observation.VerifiedAt.Add(endpoints.DefaultHealthTTL)) {
		return false
	}
	observedHost, err := serviceaddress.NormalizeHost(observation.DialHost)
	return err == nil && observedHost == dialHost
}
