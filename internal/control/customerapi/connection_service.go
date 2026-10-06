package customerapi

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

// ConnectionReader must return a consistent, customer-scoped snapshot. There
// is no production implementation yet: absence of this reader fails closed.
type ConnectionReader interface {
	ListConnectionEvidence(context.Context, string) ([]ConnectionEvidence, error)
}

type ConnectionEvidence struct {
	Rule               forwarding.Rule
	Pool               endpoints.EndpointPool
	Binding            vlessidentity.CredentialRecord
	ActiveBindingCount int
	ActivePoolCount    int
	Members            []ConnectionMemberEvidence
	Gateway            GatewayEvidence
}

type ConnectionMemberEvidence struct {
	Member                    endpoints.EndpointPoolMember
	DeploymentReceiptVerified bool
	DeployedRuleRevision      int64
	ProtocolHealthExpiresAt   time.Time
}

type GatewayEvidence struct {
	Published             bool
	PublishedRuleRevision int64
	CandidateNodeIDs      []string
}

type ConnectionView struct {
	RuleID   string `json:"rule_id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Status   string `json:"status"`
	Endpoint string `json:"endpoint,omitempty"`
	URI      string `json:"uri,omitempty"`
}

type ConnectionService struct {
	identity *customeridentity.Service
	reader   ConnectionReader
	now      func() time.Time
}

func NewConnectionService(identity *customeridentity.Service, reader ConnectionReader, now func() time.Time) *ConnectionService {
	if now == nil {
		now = time.Now
	}
	return &ConnectionService{identity: identity, reader: reader, now: now}
}

func (service *ConnectionService) List(ctx context.Context, principal customeridentity.Principal) ([]ConnectionView, error) {
	profile, err := service.identity.Me(ctx, principal)
	if err != nil {
		return nil, err
	}
	if service.reader == nil {
		return service.unverified(ctx, principal)
	}
	records, err := service.reader.ListConnectionEvidence(ctx, principal.CustomerID)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, record := range records {
		if record.Rule.CustomerID != principal.CustomerID {
			return nil, errInternal
		}
		counts[record.Rule.ID]++
	}
	views := make([]ConnectionView, 0, len(counts))
	seen := make(map[string]bool)
	for _, record := range records {
		if seen[record.Rule.ID] {
			continue
		}
		seen[record.Rule.ID] = true
		view := ConnectionView{RuleID: record.Rule.ID, Name: record.Rule.Name, Protocol: "vless+reality+vision", Status: "unavailable"}
		if counts[record.Rule.ID] == 1 {
			service.populate(&view, profile, record)
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].RuleID < views[j].RuleID })
	return views, nil
}

func (service *ConnectionService) unverified(ctx context.Context, principal customeridentity.Principal) ([]ConnectionView, error) {
	placeholders, err := service.identity.Subscriptions(ctx, principal)
	if err != nil {
		return nil, err
	}
	views := make([]ConnectionView, 0)
	seen := make(map[string]bool)
	for _, placeholder := range placeholders {
		if placeholder.Protocol != "vless" || placeholder.RuleID == "" || seen[placeholder.RuleID] {
			continue
		}
		seen[placeholder.RuleID] = true
		views = append(views, ConnectionView{RuleID: placeholder.RuleID, Name: placeholder.Name, Protocol: "vless+reality+vision", Status: "unavailable"})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].RuleID < views[j].RuleID })
	return views, nil
}

func (service *ConnectionService) populate(view *ConnectionView, profile customeridentity.Profile, evidence ConnectionEvidence) {
	rule, pool, binding := evidence.Rule, evidence.Pool, evidence.Binding
	if profile.EffectiveStatus != customers.StatusActive || rule.ID == "" || rule.Paused || !rule.Deployed ||
		rule.Status != forwarding.StatusPendingActivation || rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality ||
		rule.Protocol != forwarding.ProtocolTCP || rule.IngressReadiness() != forwarding.IngressReady ||
		rule.VLESSFlow != "xtls-rprx-vision" || rule.RealityDestination == "" ||
		evidence.ActiveBindingCount != 1 || evidence.ActivePoolCount != 1 ||
		pool.RuleID != rule.ID || pool.GroupID != rule.EntryGroupID || pool.Protocol != "vless" ||
		pool.Mode != endpoints.ModeSingleServiceEndpoint || pool.Port != rule.ListenPort ||
		binding.Binding.State != vlessidentity.StateActive || binding.Binding.CustomerID != profile.ID ||
		binding.Binding.ForwardingRuleID != rule.ID || binding.Binding.EndpointPoolID != pool.ID ||
		!evidence.Gateway.Published || evidence.Gateway.PublishedRuleRevision != rule.Revision ||
		len(evidence.Gateway.CandidateNodeIDs) == 0 {
		return
	}
	eligible := make(map[string]bool)
	now := service.now().UTC()
	for _, member := range evidence.Members {
		if member.Member.PoolID != pool.ID || member.Member.GroupID != pool.GroupID || member.Member.NodeID == "" ||
			!member.DeploymentReceiptVerified || member.DeployedRuleRevision != rule.Revision ||
			!member.Member.CandidateEligibleForNewConnection(now, endpoints.DefaultHealthTTL) ||
			!member.ProtocolHealthExpiresAt.After(now) {
			continue
		}
		eligible[member.Member.NodeID] = true
	}
	for _, nodeID := range evidence.Gateway.CandidateNodeIDs {
		if !eligible[nodeID] {
			return
		}
	}
	generated, err := provisioningvless.URI(provisioningvless.Profile{
		Endpoint: provisioningvless.Endpoint{Hostname: pool.Hostname, Port: pool.Port, Name: pool.Name},
		Identity: provisioningvless.Identity{UUID: binding.CredentialUUID, Flow: rule.VLESSFlow},
		Reality: &provisioningvless.RealityProfile{
			ServerName: rule.RealityServerName, PublicKey: rule.RealityPublicKey,
			ShortID: rule.RealityShortID, Destination: rule.RealityDestination, Fingerprint: "chrome",
		},
	})
	if err != nil || !strings.HasPrefix(generated, "vless://") {
		return
	}
	view.Status = "ready"
	view.Endpoint = net.JoinHostPort(pool.Hostname, strconv.Itoa(pool.Port))
	view.URI = generated
}
