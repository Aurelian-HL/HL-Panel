// Package subscriptions owns stable, published client subscriptions. Drafts
// and published lines are separate; credentials never appear in list responses.
package subscriptions

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"time"
)

type Line struct {
	Name      string `json:"name"`
	BindingID string `json:"binding_id,omitempty"`
	URI       string `json:"uri,omitempty"`
}
type Item struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	CustomerID         string    `json:"customer_id"`
	State              string    `json:"state"`
	Revision           int64     `json:"revision"`
	PublishedRevision  int64     `json:"published_revision"`
	PendingUpdate      bool      `json:"pending_update"`
	LineCount          int       `json:"line_count"`
	PublishedLineCount int       `json:"published_line_count"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Record is confidential. Persist it through an explicit private DTO only.
type Record struct {
	Item      Item
	OwnerID   string `json:"-"`
	Token     string `json:"-"`
	Draft     []Line `json:"-"`
	Published []Line `json:"-"`
}
type Request struct {
	Name       string `json:"name"`
	CustomerID string `json:"customer_id"`
	Lines      []Line `json:"lines"`
	Revision   int64  `json:"revision"`
}
type Command struct {
	ID, AdministratorID, Operation, IdempotencyKey, RequestSHA256, Token string
	Request                                                              Request
	At                                                                   time.Time
}
type Repository interface {
	ListSubscriptions(context.Context, string) ([]Item, error)
	Subscription(context.Context, string, string) (Record, error)
	SubscriptionByToken(context.Context, string) (Record, error)
	MutateSubscription(context.Context, Command, audit.Event) (Item, bool, error)
	Customer(context.Context, string) (customers.Customer, error)
}
type NativeResolver interface {
	ResolveSubscriptionLine(context.Context, string, string, string) (string, error)
}
