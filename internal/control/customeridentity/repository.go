package customeridentity

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
)

type CustomerRecord struct {
	Customer      customers.Customer
	UserGroupName string
}

type RuleRecord struct {
	CustomerID       string
	ID               string
	Name             string
	IngressProtocol  string
	Protocol         string
	ConnectHost      string
	ListenPort       int
	RouteDescription string
	Targets          []TargetView
	Paused           bool
	Status           string
	Deployed         bool
	Revision         int64
}

type UsageRecord struct {
	CustomerID        string
	TrafficUsedBytes  int64
	TrafficLimitBytes int64
	UpdatedAt         time.Time
}

type SubscriptionRecord struct {
	CustomerID string
	ID         string
	RuleID     string
	Name       string
	Protocol   string
	Endpoint   string
	Status     string
	Ready      bool
	URI        string
}

type ChangePasswordInput struct {
	CustomerID        string
	ExpectedRevision  int64
	PasswordHash      []byte
	KeepSessionHash   string
	IdempotencyKey    string
	RequestHMACSHA256 string
	UpdatedAt         time.Time
}

// Repository methods are deliberately customer-scoped. Implementations must
// persist the session/audit or password/audit changes atomically.
type Repository interface {
	CustomerByUsername(context.Context, string) (CustomerRecord, error)
	CustomerByID(context.Context, string) (CustomerRecord, error)
	CreateSession(context.Context, Session, audit.Event) error
	SessionByTokenHash(context.Context, string, time.Time) (Session, error)
	RevokeSession(context.Context, string, string, audit.Event) error
	PasswordChangeReplay(context.Context, string, string, string) (bool, error)
	ChangePassword(context.Context, ChangePasswordInput, audit.Event) (bool, error)
	ListRulesByCustomer(context.Context, string) ([]RuleRecord, error)
	RuleOptionsByCustomer(context.Context, string) (RuleOptionsView, error)
	CustomerUsage(context.Context, string) (UsageRecord, error)
	ListSubscriptionsByCustomer(context.Context, string) ([]SubscriptionRecord, error)
	Portal(context.Context) (PortalView, error)
	AppendAudit(context.Context, audit.Event) error
}
