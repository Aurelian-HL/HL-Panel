package customers

import (
	"context"
	"fmt"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) ListCustomers(ctx context.Context) ([]Customer, error) {
	items, err := s.repository.ListCustomers(ctx)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []Customer{}
	}
	for index := range items {
		items[index] = s.customerView(items[index])
	}
	return items, nil
}

func (s *Service) Customer(ctx context.Context, id string) (Customer, error) {
	if !validID(id) {
		return Customer{}, fmt.Errorf("%w: invalid customer ID", faults.ErrValidation)
	}
	item, err := s.repository.Customer(ctx, id)
	return s.customerView(item), err
}

func (s *Service) CreateCustomer(ctx context.Context, adminID string, input CustomerInput) (Customer, bool, error) {
	input, err := normalizeCustomerInput(input, true)
	if err != nil {
		return Customer{}, false, err
	}
	requestHash, err := customerRequestFingerprint(adminID, "", input)
	if err != nil {
		return Customer{}, false, err
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		return Customer{}, false, err
	}
	id, err := idgen.New("cus")
	if err != nil {
		return Customer{}, false, err
	}
	now := s.now().UTC()
	item := customerFromInput(input)
	item.ID, item.PasswordHash = id, passwordHash
	item.CreatedAt, item.UpdatedAt, item.Revision = now, now, 1
	return s.saveCustomer(ctx, adminID, input, item, "customer.create", requestHash)
}

func (s *Service) UpdateCustomer(ctx context.Context, adminID, id string, input CustomerInput) (Customer, bool, error) {
	if !validID(id) {
		return Customer{}, false, fmt.Errorf("%w: invalid customer ID", faults.ErrValidation)
	}
	input, err := normalizeCustomerInput(input, false)
	if err != nil {
		return Customer{}, false, err
	}
	previous, err := s.repository.Customer(ctx, id)
	if err != nil {
		return Customer{}, false, err
	}
	requestHash, err := customerRequestFingerprint(adminID, id, input)
	if err != nil {
		return Customer{}, false, err
	}
	item := customerFromInput(input)
	item.ID, item.CreatedAt, item.TrafficUsedBytes = id, previous.CreatedAt, previous.TrafficUsedBytes
	item.PasswordHash = append([]byte(nil), previous.PasswordHash...)
	if input.Password != "" {
		item.PasswordHash, err = auth.HashPassword(input.Password)
		if err != nil {
			return Customer{}, false, err
		}
	}
	item.UpdatedAt, item.Revision = s.now().UTC(), input.Revision+1
	return s.saveCustomer(ctx, adminID, input, item, "customer.update", requestHash)
}

func (s *Service) saveCustomer(ctx context.Context, adminID string, input CustomerInput, item Customer, action, requestHash string) (Customer, bool, error) {
	event, err := audit.NewEvent(item.UpdatedAt, "administrator", adminID, action, "customer", item.ID, "succeeded", map[string]any{
		"username": item.Username, "user_group_id": item.UserGroupID, "disabled": item.Disabled,
		"revision": item.Revision, "password_changed": input.Password != "",
	})
	if err != nil {
		return Customer{}, false, err
	}
	stored, replayed, err := s.repository.SaveCustomer(ctx, SaveCustomerInput{
		Customer: item, ExpectedRevision: input.Revision, IdempotencyKey: input.IdempotencyKey,
		RequestSHA256: requestHash, CreatedBy: adminID,
		PasswordChanged: input.Password != "",
	}, event)
	if err != nil {
		return Customer{}, false, err
	}
	return s.customerView(stored), replayed, nil
}

func customerFromInput(input CustomerInput) Customer {
	return Customer{
		Username: input.Username, DisplayName: input.DisplayName, UserGroupID: input.UserGroupID,
		Disabled: input.Disabled, ExpiresAt: input.ExpiresAt, TrafficLimitBytes: input.TrafficLimitBytes,
		MaxRules: input.MaxRules, SpeedLimitMbps: input.SpeedLimitMbps, IPLimit: input.IPLimit,
		ConnectionLimit: input.ConnectionLimit,
	}
}

func (s *Service) customerView(item Customer) Customer {
	item.Status = item.EffectiveStatus(s.now().UTC())
	item.PasswordHash = nil
	return item
}
