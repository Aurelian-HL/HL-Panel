package forwarding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/idgen"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

type RealityDefaults struct {
	ServerName  string
	Destination string
}

func NormalizeRealityDefaults(value RealityDefaults) (RealityDefaults, error) {
	value.ServerName = strings.TrimSpace(value.ServerName)
	value.Destination = strings.TrimSpace(value.Destination)
	if value.ServerName == "" && value.Destination == "" {
		return value, nil
	}
	if value.ServerName == "" || value.Destination == "" {
		return RealityDefaults{}, fmt.Errorf("%w: automatic Reality server name and destination must be configured together", faults.ErrValidation)
	}
	host, err := serviceaddress.NormalizeHost(value.ServerName)
	if err != nil || net.ParseIP(host) != nil {
		return RealityDefaults{}, fmt.Errorf("%w: automatic Reality server name must be a DNS name", faults.ErrValidation)
	}
	destinationHost, port, err := net.SplitHostPort(value.Destination)
	if err != nil {
		return RealityDefaults{}, fmt.Errorf("%w: automatic Reality destination must be host:port", faults.ErrValidation)
	}
	destinationHost, err = serviceaddress.NormalizeHost(destinationHost)
	if err != nil {
		return RealityDefaults{}, fmt.Errorf("%w: automatic Reality destination host is invalid", faults.ErrValidation)
	}
	destinationPort, err := strconv.Atoi(port)
	if err != nil || destinationPort < 1 || destinationPort > 65535 {
		return RealityDefaults{}, fmt.Errorf("%w: automatic Reality destination port is invalid", faults.ErrValidation)
	}
	value.ServerName = host
	value.Destination = net.JoinHostPort(destinationHost, strconv.Itoa(destinationPort))
	return value, nil
}

type Service struct {
	repository    Repository
	now           func() time.Time
	reality       RealityDefaults
	probeEchoPort int
}

type ServiceOption func(*Service)

func WithProtocolProbeEchoPort(port int) ServiceOption {
	return func(s *Service) { s.probeEchoPort = port }
}

func WithRealityDefaults(value RealityDefaults) ServiceOption {
	return func(s *Service) { s.reality = value }
}

func NewService(repository Repository, now func() time.Time, options ...ServiceOption) *Service {
	if now == nil {
		now = time.Now
	}
	s := &Service{repository: repository, now: now, probeEchoPort: protocolprobe.DefaultEchoPort}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Service) Create(ctx context.Context, adminID string, request Request, idempotencyKey string) (Rule, bool, error) {
	return s.create(ctx, "administrator", adminID, request, idempotencyKey)
}

func (s *Service) CreateForAdministrator(ctx context.Context, adminID string, request Request, idempotencyKey string) (Rule, bool, error) {
	request.CustomerID = AdministratorSubjectID(adminID)
	request.OwnerKind, request.OwnerID = OwnerAdministrator, adminID
	return s.create(ctx, "administrator", adminID, request, idempotencyKey)
}

func (s *Service) CreateForCustomer(ctx context.Context, customerID string, request Request, idempotencyKey string) (Rule, bool, error) {
	request.CustomerID = customerID
	request.OwnerKind, request.OwnerID = OwnerCustomer, customerID
	return s.create(ctx, "customer", customerID, request, idempotencyKey)
}

func (s *Service) create(ctx context.Context, actorType, actorID string, request Request, idempotencyKey string) (Rule, bool, error) {
	request, err := NormalizeRequest(request)
	if err != nil {
		return Rule{}, false, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validReference(idempotencyKey) {
		return Rule{}, false, fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 non-space characters", faults.ErrValidation)
	}
	if request.Revision != 0 {
		return Rule{}, false, fmt.Errorf("%w: revision must be zero for creation", faults.ErrValidation)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return Rule{}, false, err
	}
	digest := sha256.Sum256(data)
	autoReality := request.EffectiveIngressProtocol() == IngressVLESSReality && request.RealityServerName == "" && request.RealityPublicKey == "" && request.RealityShortID == "" && request.RealityDestination == ""
	privateKey, err := s.fillAutomaticReality(&request)
	if err != nil {
		return Rule{}, false, err
	}
	id, err := idgen.New("fwd")
	if err != nil {
		return Rule{}, false, err
	}
	now := s.now().UTC()
	rule := request.rule(id, now)
	rule.Revision = 1
	createdBy := mutationActor(actorType, actorID)
	if rule.OwnerKind == OwnerAdministrator {
		createdBy = "admin-rules:" + actorID
	}
	event, err := ruleEvent(now, actorType, actorID, "forwarding_rule.create", rule)
	if err != nil {
		return Rule{}, false, err
	}
	return s.repository.CreateForwardingRule(ctx, CreateInput{Rule: rule, ProtocolProbeEchoPort: s.probeEchoPort, RealityPrivateKey: privateKey, VLESSSOCKS5Password: request.VLESSSOCKS5Password, AutoReality: autoReality, IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(digest[:]), CreatedBy: createdBy}, event)
}

func (s *Service) Update(ctx context.Context, adminID, id string, request Request, idempotencyKey string) (Rule, bool, error) {
	return s.update(ctx, "administrator", adminID, "", id, request, idempotencyKey)
}

func (s *Service) UpdateForAdministrator(ctx context.Context, adminID, id string, request Request, idempotencyKey string) (Rule, bool, error) {
	request.CustomerID = AdministratorSubjectID(adminID)
	request.OwnerKind, request.OwnerID = OwnerAdministrator, adminID
	return s.update(ctx, "administrator", adminID, request.CustomerID, id, request, idempotencyKey)
}

func (s *Service) UpdateForCustomer(ctx context.Context, customerID, id string, request Request, idempotencyKey string) (Rule, bool, error) {
	request.CustomerID = customerID
	request.OwnerKind, request.OwnerID = OwnerCustomer, customerID
	return s.update(ctx, "customer", customerID, customerID, id, request, idempotencyKey)
}

func (s *Service) update(ctx context.Context, actorType, actorID, expectedCustomerID, id string, request Request, idempotencyKey string) (Rule, bool, error) {
	id = strings.TrimSpace(id)
	if !validReference(id) {
		return Rule{}, false, fmt.Errorf("%w: rule id is required", faults.ErrValidation)
	}
	request, err := NormalizeRequest(request)
	if err != nil {
		return Rule{}, false, err
	}
	if request.Revision < 1 || request.Revision == int64(^uint64(0)>>1) {
		return Rule{}, false, fmt.Errorf("%w: revision is required and must be incrementable", faults.ErrValidation)
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validReference(idempotencyKey) {
		return Rule{}, false, fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 non-space characters", faults.ErrValidation)
	}
	data, err := json.Marshal(struct {
		ID      string
		Request Request
	}{id, request})
	if err != nil {
		return Rule{}, false, err
	}
	digest := sha256.Sum256(data)
	autoReality := request.EffectiveIngressProtocol() == IngressVLESSReality && request.RealityServerName == "" && request.RealityPublicKey == "" && request.RealityShortID == "" && request.RealityDestination == ""
	privateKey, err := s.fillAutomaticReality(&request)
	if err != nil {
		return Rule{}, false, err
	}
	now := s.now().UTC()
	rule := request.rule(id, now)
	createdBy := mutationActor(actorType, actorID)
	if rule.OwnerKind == OwnerAdministrator {
		createdBy = "admin-rules:" + actorID
	}
	// The repository preserves CreatedAt under the same transaction as CAS.
	rule.CreatedAt = time.Time{}
	rule.Revision = request.Revision + 1
	event, err := ruleEvent(now, actorType, actorID, "forwarding_rule.update", rule)
	if err != nil {
		return Rule{}, false, err
	}
	return s.repository.UpdateForwardingRule(ctx, UpdateInput{Rule: rule, ProtocolProbeEchoPort: s.probeEchoPort, RealityPrivateKey: privateKey, VLESSSOCKS5Password: request.VLESSSOCKS5Password, AutoReality: autoReality, ExpectedRevision: request.Revision, ExpectedCustomerID: expectedCustomerID, IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(digest[:]), CreatedBy: createdBy}, event)
}

func (s *Service) fillAutomaticReality(request *Request) (string, error) {
	if request.EffectiveIngressProtocol() != IngressVLESSReality || request.RealityServerName != "" || request.RealityPublicKey != "" || request.RealityShortID != "" || request.RealityDestination != "" {
		return "", nil
	}
	if s.reality.ServerName == "" || s.reality.Destination == "" {
		return "", nil
	}
	publicKey, privateKey, err := provisioningvless.GenerateRealityKeyPair(rand.Reader)
	if err != nil {
		return "", err
	}
	shortID := make([]byte, 8)
	if _, err := rand.Read(shortID); err != nil {
		return "", err
	}
	request.RealityServerName = s.reality.ServerName
	request.RealityDestination = s.reality.Destination
	request.RealityPublicKey = publicKey
	request.RealityShortID = hex.EncodeToString(shortID)
	return privateKey, nil
}

func mutationActor(actorType, actorID string) string {
	if actorType == "administrator" {
		return actorID
	}
	return actorType + ":" + actorID
}

func (s *Service) List(ctx context.Context) ([]Rule, error) {
	return s.repository.ListForwardingRules(ctx)
}
func (s *Service) ListForAdministrator(ctx context.Context, adminID string) ([]Rule, error) {
	items, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	owned := make([]Rule, 0)
	for _, item := range items {
		if item.OwnedByAdministrator(adminID) {
			owned = append(owned, item)
		}
	}
	return owned, nil
}
func (s *Service) Get(ctx context.Context, id string) (Rule, error) {
	return s.repository.ForwardingRule(ctx, strings.TrimSpace(id))
}

func (s *Service) GetForCustomer(ctx context.Context, customerID, id string) (Rule, error) {
	rule, err := s.Get(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	if !rule.OwnedByCustomer(customerID) {
		return Rule{}, faults.ErrNotFound
	}
	return rule, nil
}

func (s *Service) GetForAdministrator(ctx context.Context, adminID, id string) (Rule, error) {
	rule, err := s.Get(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	if !rule.OwnedByAdministrator(adminID) {
		return Rule{}, faults.ErrNotFound
	}
	return rule, nil
}

func (r Request) rule(id string, now time.Time) Rule {
	return Rule{ID: id, Name: r.Name, CustomerID: r.CustomerID, OwnerKind: r.OwnerKind, OwnerID: r.OwnerID, RuleGroupID: r.RuleGroupID, EntryGroupID: r.EntryGroupID, ExitGroupID: r.ExitGroupID,
		EgressMode: r.EgressMode, VLESSOutboundMode: r.VLESSOutboundMode, VLESSSOCKS5Host: r.VLESSSOCKS5Host, VLESSSOCKS5Port: r.VLESSSOCKS5Port, VLESSSOCKS5Username: r.VLESSSOCKS5Username,
		IngressProtocol: r.IngressProtocol, VLESSFlow: r.VLESSFlow,
		RealityServerName: r.RealityServerName, RealityPublicKey: r.RealityPublicKey, RealityShortID: r.RealityShortID, RealityDestination: r.RealityDestination,
		IngressStatus: r.IngressReadiness(), Protocol: r.Protocol, ListenPort: r.ListenPort, Targets: r.Targets, SelectionPolicy: r.SelectionPolicy,
		AcceptProxyProtocol: r.AcceptProxyProtocol, SendProxyProtocol: r.SendProxyProtocol, SpeedLimitMbps: r.SpeedLimitMbps,
		IPLimit: r.IPLimit, ConnectionLimit: r.ConnectionLimit, TrafficLimitBytes: r.TrafficLimitBytes,
		Paused: r.Paused, Description: r.Description, CreatedAt: now, UpdatedAt: now, Status: StatusPendingActivation, Deployed: false}
}

func ruleEvent(now time.Time, actorType, actorID, action string, rule Rule) (audit.Event, error) {
	// Targets and descriptions are deliberately absent: audit records should
	// never become an accidental secondary store of customer credentials.
	metadata := map[string]any{
		"customer_id": rule.CustomerID, "rule_group_id": rule.RuleGroupID, "entry_group_id": rule.EntryGroupID, "exit_group_id": rule.ExitGroupID,
		"egress_mode": rule.EgressMode, "ingress_protocol": rule.EffectiveIngressProtocol(), "ingress_status": rule.IngressReadiness(), "protocol": rule.Protocol, "listen_port": rule.ListenPort,
		"target_count": len(rule.Targets), "selection_policy": rule.SelectionPolicy,
		"accept_proxy_protocol": rule.AcceptProxyProtocol, "send_proxy_protocol": rule.SendProxyProtocol,
		"traffic_limit_bytes": rule.TrafficLimitBytes, "speed_limit_mbps": rule.SpeedLimitMbps, "ip_limit": rule.IPLimit, "connection_limit": rule.ConnectionLimit,
		"paused": rule.Paused, "revision": rule.Revision,
	}
	if reason := rule.PendingActivationReason(); reason != "" {
		metadata["activation_reason"] = reason
	}
	return audit.NewEvent(now, actorType, actorID, action, "forwarding_rule", rule.ID, "succeeded", metadata)
}
