// Package forwarding owns structured customer forwarding rules. Persisting a
// rule does not mean an engine has applied it.
package forwarding

import "time"

type EgressMode string

// VLESSOutboundMode controls the server-side outbound used by a VLESS
// Reality ingress. It is deliberately separate from EgressMode: the latter
// selects the NY direct/exit-group authorization path, while this field only
// selects the Xray outbound after the customer handshake.
type VLESSOutboundMode string
type Protocol string

// IngressProtocol describes what the customer-facing listener accepts. It is
// intentionally separate from Protocol, which describes the final target
// transport. A VLESS TLS ingress still forwards a TCP target.
type IngressProtocol string
type SelectionPolicy string
type SendProxyProtocol int
type Status string
type IngressStatus string
type OwnerKind string

// ActivationReason is a non-sensitive explanation for why a persisted rule
// has not been emitted into a node's executable bundle. It deliberately does
// not contain UUIDs, private keys, or other runtime material.
type ActivationReason string

const (
	OwnerCustomer       OwnerKind         = "customer"
	OwnerAdministrator  OwnerKind         = "administrator"
	EgressDirect        EgressMode        = "DIRECT"
	EgressExitGroup     EgressMode        = "EXIT_GROUP"
	VLESSOutboundDirect VLESSOutboundMode = "DIRECT"
	VLESSOutboundSOCKS5 VLESSOutboundMode = "SOCKS5"
	ProtocolTCP         Protocol          = "tcp"
	ProtocolUDP         Protocol          = "udp"
	IngressTCP          IngressProtocol   = "tcp"
	IngressUDP          IngressProtocol   = "udp"
	// IngressSOCKS5 is the NY SOCKS5 customer-facing listener. It is kept
	// separate from the raw NY TCP listener because the client handshake and
	// runtime adapter are different protocols.
	IngressSOCKS5       IngressProtocol = "socks5"
	IngressVLESSReality IngressProtocol = "vless_reality"
	// IngressVLESSTLS is retained as a source-level alias for early callers;
	// serialized requests always use the Reality/ Vision value above.
	IngressVLESSTLS         IngressProtocol   = IngressVLESSReality
	SelectionRoundRobin     SelectionPolicy   = "round_robin"
	SelectionRandom         SelectionPolicy   = "random"
	SelectionIPHash         SelectionPolicy   = "ip_hash"
	SelectionLeastLoad      SelectionPolicy   = "least_load"
	SelectionFailover       SelectionPolicy   = "failover"
	SendProxyDisabled       SendProxyProtocol = 0
	SendProxyV1TCP          SendProxyProtocol = 1
	SendProxyV2TCPUDP       SendProxyProtocol = 2
	SendProxyV2TCP          SendProxyProtocol = 3
	StatusPaused            Status            = "paused"
	StatusCustomerDisabled  Status            = "customer_disabled"
	StatusCustomerExpired   Status            = "customer_expired"
	StatusQuotaExhausted    Status            = "quota_exhausted"
	StatusPendingActivation Status            = "pending_activation"
	// StatusActive means the rule has a matching deployment receipt and a
	// verified healthy runtime candidate. It is distinct from customer
	// availability so pause/expiry/quota states can still override it.
	StatusActive          Status        = "active"
	IngressReady          IngressStatus = "ready"
	IngressPendingReality IngressStatus = "pending_reality_parameters"

	// ActivationReasonVLESSRuntimeMaterialPending is used by the current
	// control-plane slice while VLESS credentials and node-local Reality
	// private material are not yet connected to bundle compilation. A rule may
	// be saved, but it must remain pending and never be represented as GOST or
	// DIRECT traffic.
	ActivationReasonVLESSRuntimeMaterialPending ActivationReason = "vless_runtime_material_pending"
	// The current node bundle compiler has a GOST TCP adapter only. Persist a
	// SOCKS5 rule, but keep it pending until a dedicated SOCKS5 adapter is
	// connected instead of silently publishing it as raw TCP.
	ActivationReasonSOCKS5RuntimeMaterialPending ActivationReason = "socks5_runtime_material_pending"
	ActivationReasonExitGroupUnsupported         ActivationReason = "exit_group_engine_unsupported"
	ActivationReasonUDPUnsupported               ActivationReason = "udp_engine_unsupported"
	ActivationReasonAdvancedOptionsUnsupported   ActivationReason = "advanced_options_engine_unsupported"
	ActivationReasonSelectionUnsupported         ActivationReason = "selection_engine_unsupported"
)

type Target struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type Rule struct {
	ID                  string            `json:"id"`
	OwnerKind           OwnerKind         `json:"owner_kind,omitempty"`
	OwnerID             string            `json:"owner_id,omitempty"`
	Name                string            `json:"name"`
	CustomerID          string            `json:"customer_id"`
	RuleGroupID         string            `json:"rule_group_id"`
	EntryGroupID        string            `json:"entry_group_id"`
	ExitGroupID         string            `json:"exit_group_id"`
	EgressMode          EgressMode        `json:"egress_mode"`
	VLESSOutboundMode   VLESSOutboundMode `json:"vless_outbound_mode,omitempty"`
	VLESSSOCKS5Host     string            `json:"vless_socks5_host,omitempty"`
	VLESSSOCKS5Port     int               `json:"vless_socks5_port,omitempty"`
	VLESSSOCKS5Username string            `json:"vless_socks5_username,omitempty"`
	IngressProtocol     IngressProtocol   `json:"ingress_protocol"`
	VLESSFlow           string            `json:"vless_flow,omitempty"`
	RealityServerName   string            `json:"reality_server_name,omitempty"`
	RealityPublicKey    string            `json:"reality_public_key,omitempty"`
	RealityShortID      string            `json:"reality_short_id,omitempty"`
	RealityDestination  string            `json:"reality_destination,omitempty"`
	Protocol            Protocol          `json:"protocol"`
	ListenPort          int               `json:"listen_port"`
	Targets             []Target          `json:"targets"`
	SelectionPolicy     SelectionPolicy   `json:"selection_policy"`
	AcceptProxyProtocol bool              `json:"accept_proxy_protocol,omitempty"`
	SendProxyProtocol   SendProxyProtocol `json:"send_proxy_protocol,omitempty"`
	SpeedLimitMbps      int               `json:"speed_limit_mbps,omitempty"`
	IPLimit             int               `json:"ip_limit,omitempty"`
	ConnectionLimit     int               `json:"connection_limit,omitempty"`
	TrafficLimitBytes   int64             `json:"traffic_limit_bytes,omitempty"`
	TrafficUsedBytes    int64             `json:"traffic_used_bytes,omitempty"`
	TrafficQuotaMonthly bool              `json:"traffic_quota_monthly,omitempty"`
	TrafficUsagePeriod  string            `json:"traffic_usage_period,omitempty"`
	Paused              bool              `json:"paused"`
	Description         string            `json:"description"`
	Revision            int64             `json:"revision"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	Status              Status            `json:"status"`
	IngressStatus       IngressStatus     `json:"ingress_status"`
	Deployed            bool              `json:"deployed"`
	ActivationReason    ActivationReason  `json:"activation_reason,omitempty"`
}

func AdministratorSubjectID(adminID string) string { return "adminline_" + adminID }

func (r Rule) OwnedByAdministrator(adminID string) bool {
	return r.OwnerKind == OwnerAdministrator && r.OwnerID == adminID && r.CustomerID == AdministratorSubjectID(adminID)
}

func (r Rule) OwnedByCustomer(customerID string) bool {
	return r.OwnerKind != OwnerAdministrator && r.CustomerID == customerID
}

// TrafficQuotaPeriodBounds returns the half-open UTC natural-month interval
// used by monthly rule quotas. Keeping this in the forwarding package makes
// ledger queries and quota snapshots use exactly the same boundary.
func TrafficQuotaPeriodBounds(at time.Time) (time.Time, time.Time) {
	at = at.UTC()
	start := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

func TrafficQuotaPeriod(at time.Time) string {
	start, _ := TrafficQuotaPeriodBounds(at)
	return start.Format("2006-01")
}

// RuleTrafficProjection binds a ledger query to the rule version and accounting
// scope it read. An edit during that query invalidates the projection.
type RuleTrafficProjection struct {
	RuleID           string
	ExpectedRevision int64
	Monthly          bool
	TotalBytes       int64
	At               time.Time
}

// PendingActivationReason reports a bounded, non-secret reason for a rule
// which the current node bundle compiler cannot safely activate. Empty means
// the rule is either supported by the current compiler or is blocked by a
// different lifecycle condition (customer, pause, quota, and so on).
func (r Rule) PendingActivationReason() ActivationReason {
	if r.EgressMode == EgressExitGroup {
		return ActivationReasonExitGroupUnsupported
	}
	if r.EffectiveIngressProtocol() == IngressUDP || r.Protocol == ProtocolUDP {
		return ActivationReasonUDPUnsupported
	}
	if r.EffectiveIngressProtocol() == IngressSOCKS5 {
		return ActivationReasonSOCKS5RuntimeMaterialPending
	}
	if r.AcceptProxyProtocol || r.SendProxyProtocol != SendProxyDisabled ||
		r.SpeedLimitMbps != 0 || r.IPLimit != 0 || r.ConnectionLimit != 0 {
		return ActivationReasonAdvancedOptionsUnsupported
	}
	if r.SelectionPolicy == SelectionLeastLoad {
		return ActivationReasonSelectionUnsupported
	}
	if r.EffectiveIngressProtocol() == IngressVLESSReality && r.IngressReadiness() == IngressReady {
		return ActivationReasonVLESSRuntimeMaterialPending
	}
	return ""
}

// Request is shared by create and replace. ListenPort zero asks the repository
// to allocate a free port atomically. Revision is required for replacement.
type Request struct {
	Name                string            `json:"name"`
	CustomerID          string            `json:"customer_id"`
	OwnerKind           OwnerKind         `json:"owner_kind,omitempty"`
	OwnerID             string            `json:"owner_id,omitempty"`
	RuleGroupID         string            `json:"rule_group_id"`
	EntryGroupID        string            `json:"entry_group_id"`
	ExitGroupID         string            `json:"exit_group_id"`
	EgressMode          EgressMode        `json:"egress_mode"`
	VLESSOutboundMode   VLESSOutboundMode `json:"vless_outbound_mode,omitempty"`
	VLESSSOCKS5Host     string            `json:"vless_socks5_host,omitempty"`
	VLESSSOCKS5Port     int               `json:"vless_socks5_port,omitempty"`
	VLESSSOCKS5Username string            `json:"vless_socks5_username,omitempty"`
	// VLESSSOCKS5Password is accepted only on write requests. It is never
	// copied into Rule, snapshots' public projections, audit events, or exports.
	VLESSSOCKS5Password string            `json:"vless_socks5_password,omitempty"`
	IngressProtocol     IngressProtocol   `json:"ingress_protocol"`
	VLESSFlow           string            `json:"vless_flow,omitempty"`
	RealityServerName   string            `json:"reality_server_name,omitempty"`
	RealityPublicKey    string            `json:"reality_public_key,omitempty"`
	RealityShortID      string            `json:"reality_short_id,omitempty"`
	RealityDestination  string            `json:"reality_destination,omitempty"`
	Protocol            Protocol          `json:"protocol"`
	ListenPort          int               `json:"listen_port"`
	Targets             []Target          `json:"targets"`
	SelectionPolicy     SelectionPolicy   `json:"selection_policy"`
	AcceptProxyProtocol bool              `json:"accept_proxy_protocol,omitempty"`
	SendProxyProtocol   SendProxyProtocol `json:"send_proxy_protocol,omitempty"`
	SpeedLimitMbps      int               `json:"speed_limit_mbps,omitempty"`
	IPLimit             int               `json:"ip_limit,omitempty"`
	ConnectionLimit     int               `json:"connection_limit,omitempty"`
	TrafficLimitBytes   int64             `json:"traffic_limit_bytes,omitempty"`
	TrafficQuotaMonthly bool              `json:"traffic_quota_monthly,omitempty"`
	Paused              bool              `json:"paused"`
	Description         string            `json:"description"`
	Revision            int64             `json:"revision"`
}

// EffectiveIngressProtocol keeps snapshots and older clients which omitted
// the field source-compatible while making new writes explicit.
func (r Rule) EffectiveIngressProtocol() IngressProtocol {
	if r.IngressProtocol != "" {
		return r.IngressProtocol
	}
	if r.Protocol == ProtocolUDP {
		return IngressUDP
	}
	return IngressTCP
}

func (r Request) EffectiveIngressProtocol() IngressProtocol {
	if r.IngressProtocol != "" {
		return r.IngressProtocol
	}
	if r.Protocol == ProtocolUDP {
		return IngressUDP
	}
	return IngressTCP
}

// IngressReadiness reports whether the customer-facing protocol has all
// non-secret parameters required by the current control-plane slice. It does
// not imply that an engine is running or that a credential has been issued.
func (r Request) IngressReadiness() IngressStatus {
	if r.EffectiveIngressProtocol() != IngressVLESSReality {
		return IngressReady
	}
	if r.RealityServerName == "" || r.RealityPublicKey == "" || r.RealityShortID == "" {
		return IngressPendingReality
	}
	return IngressReady
}

func (r Rule) IngressReadiness() IngressStatus {
	return Request{
		IngressProtocol: r.EffectiveIngressProtocol(), VLESSFlow: r.VLESSFlow,
		RealityServerName: r.RealityServerName, RealityPublicKey: r.RealityPublicKey,
		RealityShortID: r.RealityShortID, RealityDestination: r.RealityDestination,
	}.IngressReadiness()
}

// CustomerAvailability is a minimal, non-secret projection of the customer.
// Zero TrafficLimitBytes means unlimited traffic.
type CustomerAvailability struct {
	Enabled           bool
	ExpiresAt         *time.Time
	TrafficLimitBytes int64
	UsedTrafficBytes  int64
}

func DeriveStatus(rule Rule, customer CustomerAvailability, now time.Time) Status {
	if rule.Paused {
		return StatusPaused
	}
	if rule.TrafficLimitBytes > 0 && rule.TrafficUsedBytes >= rule.TrafficLimitBytes {
		return StatusQuotaExhausted
	}
	if !customer.Enabled {
		return StatusCustomerDisabled
	}
	if customer.ExpiresAt != nil && !customer.ExpiresAt.After(now) {
		return StatusCustomerExpired
	}
	if customer.TrafficLimitBytes > 0 && customer.UsedTrafficBytes >= customer.TrafficLimitBytes {
		return StatusQuotaExhausted
	}
	if rule.Deployed {
		return StatusActive
	}
	return StatusPendingActivation
}
