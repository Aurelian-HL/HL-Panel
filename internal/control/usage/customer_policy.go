package usage

import (
	"context"
	"errors"
	"strings"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

type CustomerReader interface {
	Customer(context.Context, string) (customers.Customer, error)
}

type AdministratorReader interface {
	AdministratorByID(context.Context, string) (auth.Administrator, error)
}

// CustomerPolicyAdapter lets the usage service consume the existing customer
// lifecycle without coupling its normalized ledger to the snapshot store.
type CustomerPolicyAdapter struct{ reader CustomerReader }

func NewCustomerPolicyAdapter(reader CustomerReader) *CustomerPolicyAdapter {
	return &CustomerPolicyAdapter{reader: reader}
}

func (adapter *CustomerPolicyAdapter) UsagePolicy(ctx context.Context, customerID string) (CustomerPolicy, error) {
	item, err := adapter.reader.Customer(ctx, customerID)
	if err != nil {
		if errors.Is(err, faults.ErrNotFound) {
			adminErr := administratorSubject(ctx, adapter.reader, customerID)
			if adminErr == nil {
				return CustomerPolicy{CustomerID: customerID}, nil
			}
			if !errors.Is(adminErr, faults.ErrNotFound) {
				return CustomerPolicy{}, adminErr
			}
		}
		return CustomerPolicy{}, err
	}
	return CustomerPolicy{
		CustomerID: item.ID, Disabled: item.Disabled, ExpiresAt: item.ExpiresAt,
		TrafficLimitBytes: item.TrafficLimitBytes,
	}, nil
}

func administratorSubject(ctx context.Context, reader CustomerReader, subjectID string) error {
	adminID, ok := strings.CutPrefix(subjectID, "adminline_")
	if !ok || adminID == "" || forwarding.AdministratorSubjectID(adminID) != subjectID {
		return faults.ErrNotFound
	}
	admins, ok := reader.(AdministratorReader)
	if !ok {
		return faults.ErrNotFound
	}
	admin, err := admins.AdministratorByID(ctx, adminID)
	if err != nil {
		return err
	}
	if admin.ID != adminID {
		return faults.ErrNotFound
	}
	return nil
}
