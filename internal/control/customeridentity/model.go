// Package customeridentity owns end-user authentication and the projections a
// customer may read about their own account. It is intentionally separate from
// administrator authentication and never accepts a customer ID from HTTP query
// parameters for self-service reads.
package customeridentity

import (
	"time"

	"github.com/hongle/hl-panel/internal/control/customers"
)

type Session struct {
	ID         string
	CustomerID string
	TokenHash  string
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

type Principal struct {
	CustomerID       string
	SessionID        string
	SessionTokenHash string
}

type Profile struct {
	ID                string           `json:"id"`
	Username          string           `json:"username"`
	DisplayName       string           `json:"display_name"`
	UserType          string           `json:"user_type"`
	UserGroupName     string           `json:"user_group_name"`
	ExpiresAt         *time.Time       `json:"expires_at"`
	TrafficUsedBytes  int64            `json:"traffic_used_bytes"`
	TrafficLimitBytes int64            `json:"traffic_limit_bytes"`
	MaxRules          int              `json:"max_rules"`
	SpeedLimitMbps    int              `json:"speed_limit_mbps"`
	IPLimit           int              `json:"ip_limit"`
	ConnectionLimit   int              `json:"connection_limit"`
	EffectiveStatus   customers.Status `json:"effective_status"`
}

type LoginResult struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	User        Profile   `json:"user"`
}

type TargetView struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// RuleView omits internal topology IDs and engine configuration. Customers see
// only their usable connection address, destinations, and lifecycle result.
type RuleView struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	// Protocol is the wire transport (TCP/UDP); IngressProtocol is the
	// customer-facing handshake and must remain distinct for SOCKS5/VLESS.
	IngressProtocol   string       `json:"ingress_protocol"`
	Protocol         string       `json:"protocol"`
	ConnectHost      string       `json:"connect_host"`
	ListenPort       int          `json:"listen_port"`
	RouteDescription string       `json:"route_description"`
	Targets          []TargetView `json:"targets"`
	Paused           bool         `json:"paused"`
	Status           string       `json:"status"`
	Deployed         bool         `json:"deployed"`
	Revision         int64        `json:"revision"`
}

type RuleGroupOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PortRangeOption struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type EntryGroupOption struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	ConnectHost         string            `json:"connect_host"`
	PortRanges          []PortRangeOption `json:"port_ranges"`
	AllowDirect         bool              `json:"allow_direct"`
	AllowedExitGroupIDs []string          `json:"allowed_exit_group_ids"`
}

type RuleOptionsView struct {
	EntryGroups []EntryGroupOption `json:"entry_groups"`
	ExitGroups  []RuleGroupOption  `json:"exit_groups"`
	RuleGroups  []RuleGroupOption  `json:"rule_groups"`
}

type UsageView struct {
	TrafficUsedBytes  int64     `json:"traffic_used_bytes"`
	TrafficLimitBytes int64     `json:"traffic_limit_bytes"`
	RemainingBytes    *int64    `json:"remaining_bytes"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type SubscriptionView struct {
	ID       string `json:"id"`
	RuleID   string `json:"rule_id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
	URI      string `json:"uri,omitempty"`
}

type AnnouncementView struct {
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// PortalView is a deliberately public-safe projection. It must never contain
// database addresses, license keys, node credentials, or internal topology.
type PortalView struct {
	SiteName     string           `json:"site_name"`
	PanelVersion string           `json:"panel_version"`
	Announcement AnnouncementView `json:"announcement"`
	SiteInfo     []InfoItem       `json:"site_info"`
	BackendInfo  []InfoItem       `json:"backend_info"`
}

type InfoItem struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
