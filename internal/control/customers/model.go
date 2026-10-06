package customers

import "time"

type Status string

const (
	StatusActive         Status = "active"
	StatusDisabled       Status = "disabled"
	StatusExpired        Status = "expired"
	StatusQuotaExhausted Status = "quota_exhausted"
)

// Customer is an end-user account, never an administrator identity. Usage is
// reserved for verified accounting ingestion; operator forms cannot write it.
type Customer struct {
	ID                string     `json:"id"`
	Username          string     `json:"username"`
	DisplayName       string     `json:"display_name"`
	UserGroupID       string     `json:"user_group_id"`
	Disabled          bool       `json:"disabled"`
	ExpiresAt         *time.Time `json:"expires_at"`
	TrafficLimitBytes int64      `json:"traffic_limit_bytes"`
	TrafficUsedBytes  int64      `json:"traffic_used_bytes"`
	MaxRules          int        `json:"max_rules"`
	SpeedLimitMbps    int        `json:"speed_limit_mbps"`
	IPLimit           int        `json:"ip_limit"`
	ConnectionLimit   int        `json:"connection_limit"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Revision          int64      `json:"revision"`
	Status            Status     `json:"effective_status"`
	PasswordHash      []byte     `json:"-"`
}


func (c Customer) EffectiveStatus(now time.Time) Status {
	if c.Disabled {
		return StatusDisabled
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(now) {
		return StatusExpired
	}
	if c.TrafficLimitBytes > 0 && c.TrafficUsedBytes >= c.TrafficLimitBytes {
		return StatusQuotaExhausted
	}
	return StatusActive
}

type UserGroup struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	AllowedEntryGroupIDs []string  `json:"allowed_entry_group_ids"`
	AllowedExitGroupIDs  []string  `json:"allowed_exit_group_ids"`
	AllowDirect          bool      `json:"allow_direct"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	Revision             int64     `json:"revision"`
}

type CustomerInput struct {
	Username          string     `json:"username"`
	DisplayName       string     `json:"display_name"`
	UserGroupID       string     `json:"user_group_id"`
	Password          string     `json:"password"`
	Disabled          bool       `json:"disabled"`
	ExpiresAt         *time.Time `json:"expires_at"`
	TrafficLimitBytes int64      `json:"traffic_limit_bytes"`
	MaxRules          int        `json:"max_rules"`
	SpeedLimitMbps    int        `json:"speed_limit_mbps"`
	IPLimit           int        `json:"ip_limit"`
	ConnectionLimit   int        `json:"connection_limit"`
	Revision          int64      `json:"revision"`
	IdempotencyKey    string     `json:"-"`
}

type UserGroupInput struct {
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	AllowedEntryGroupIDs []string `json:"allowed_entry_group_ids"`
	AllowedExitGroupIDs  []string `json:"allowed_exit_group_ids"`
	AllowDirect          bool     `json:"allow_direct"`
	Revision             int64    `json:"revision"`
	IdempotencyKey       string   `json:"-"`
}
