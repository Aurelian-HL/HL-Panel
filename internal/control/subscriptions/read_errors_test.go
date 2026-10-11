package subscriptions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

type failingReadRepository struct {
	Repository
	record Record
	err    error
}

func (r failingReadRepository) Customer(context.Context, string) (customers.Customer, error) {
	return customers.Customer{}, r.err
}
func (r failingReadRepository) SubscriptionByToken(context.Context, string) (Record, error) {
	return r.record, r.err
}

type failingResolver struct{ err error }

func (r failingResolver) ResolveSubscriptionLine(context.Context, string, string, string) (string, error) {
	return "", r.err
}

func TestSubscriptionReadOutagesAreRetryableRatherThanRevocations(t *testing.T) {
	outage := errors.New("isolated repository timeout")
	for _, err := range []error{outage, faults.ErrNotFound} {
		s := NewService(failingReadRepository{err: err}, nil, nil)
		_, customerErr := s.Resolve(context.Background(), Record{Item: Item{CustomerID: "customer"}}, false)
		_, _, lookupErr := s.Public(context.Background(), "sub_"+strings.Repeat("a", 43))
		for _, got := range []error{customerErr, lookupErr} {
			if errors.Is(err, faults.ErrNotFound) {
				if !errors.Is(got, faults.ErrNotFound) || errors.Is(got, faults.ErrUnavailable) {
					t.Fatal(got)
				}
			} else if !errors.Is(got, faults.ErrUnavailable) || !errors.Is(got, outage) || errors.Is(got, faults.ErrNotFound) {
				t.Fatal(got)
			}
		}
	}
}

func TestNativeSubscriptionOutageDoesNotProducePartialFeed(t *testing.T) {
	record := Record{OwnerID: "admin", Item: Item{ForwardingRuleID: "rule", CustomerID: forwarding.AdministratorSubjectID("admin")}, Published: []Line{{Name: "static", URI: testURI}, {Name: "native", BindingID: "binding"}}}
	lines, err := NewService(nil, failingResolver{errors.New("database unavailable")}, nil).Resolve(context.Background(), record, false)
	if len(lines) != 0 || !errors.Is(err, faults.ErrUnavailable) {
		t.Fatal("outage returned a partial client feed", lines, err)
	}
	lines, err = NewService(nil, failingResolver{faults.ErrConflict}, nil).Resolve(context.Background(), record, false)
	if err != nil || len(lines) != 2 || lines[1].Error == "" {
		t.Fatal("business readiness classification changed", err)
	}
}
