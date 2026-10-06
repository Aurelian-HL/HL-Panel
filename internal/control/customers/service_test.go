package customers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type recordingRepository struct {
	item           Customer
	group          UserGroup
	customerWrites []SaveCustomerInput
	groupWrites    []SaveUserGroupInput
	events         []audit.Event
}

func (r *recordingRepository) ListCustomers(context.Context) ([]Customer, error) {
	return []Customer{r.item}, nil
}
func (r *recordingRepository) Customer(context.Context, string) (Customer, error) { return r.item, nil }
func (r *recordingRepository) ListUserGroups(context.Context) ([]UserGroup, error) {
	return []UserGroup{r.group}, nil
}
func (r *recordingRepository) UserGroup(context.Context, string) (UserGroup, error) {
	return r.group, nil
}
func (r *recordingRepository) SaveCustomer(_ context.Context, input SaveCustomerInput, event audit.Event) (Customer, bool, error) {
	r.customerWrites = append(r.customerWrites, input)
	r.events = append(r.events, event)
	r.item = input.Customer
	return r.item, false, nil
}
func (r *recordingRepository) SaveUserGroup(_ context.Context, input SaveUserGroupInput, event audit.Event) (UserGroup, bool, error) {
	r.groupWrites = append(r.groupWrites, input)
	r.events = append(r.events, event)
	r.group = input.UserGroup
	return r.group, false, nil
}

func validCustomerInput() CustomerInput {
	return CustomerInput{Username: "Customer.A", DisplayName: "张三", UserGroupID: "ugrp_1", Password: "1", IdempotencyKey: "create-customer-1"}
}

func TestCustomerPasswordsAreHashedPreservedAndNeverReturned(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	repo := &recordingRepository{}
	service := NewService(repo, func() time.Time { return now })
	input := validCustomerInput()
	created, _, err := service.CreateCustomer(ctx, "admin_1", input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Username != "customer.a" || created.Status != StatusActive || created.Revision != 1 || !created.CreatedAt.Equal(now) {
		t.Fatalf("unexpected created customer: %#v", created)
	}
	if len(created.PasswordHash) != 0 {
		t.Fatal("service returned password hash")
	}
	storedHash := append([]byte(nil), repo.item.PasswordHash...)
	if err := auth.ValidatePasswordHash(storedHash); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(storedHash, []byte(input.Password)) {
		t.Fatal("plaintext password stored")
	}
	repo.item.TrafficUsedBytes = 77
	input.Password, input.Revision, input.IdempotencyKey = "", 1, "update-customer-1"
	input.DisplayName = "修改名称"
	updated, _, err := service.UpdateCustomer(ctx, "admin_1", created.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.TrafficUsedBytes != 77 || !updated.CreatedAt.Equal(created.CreatedAt) || updated.Revision != 2 {
		t.Fatal("update lost system-owned fields")
	}
	if !bytes.Equal(repo.item.PasswordHash, storedHash) {
		t.Fatal("empty password changed stored password")
	}
	input.Password, input.Revision, input.IdempotencyKey = "2", 2, "update-customer-2"
	if _, _, err := service.UpdateCustomer(ctx, "admin_1", created.ID, input); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(repo.item.PasswordHash, storedHash) {
		t.Fatal("nonempty password did not rotate hash")
	}
	listed, err := service.ListCustomers(ctx)
	if err != nil || len(listed) != 1 || len(listed[0].PasswordHash) != 0 {
		t.Fatal("list exposed hash or failed")
	}
	got, err := service.Customer(ctx, created.ID)
	if err != nil || len(got.PasswordHash) != 0 {
		t.Fatal("get exposed hash or failed")
	}
	raw, _ := json.Marshal(repo.events)
	if bytes.Contains(raw, storedHash) || bytes.Contains(raw, []byte("pbkdf2")) || bytes.Contains(raw, []byte(`"password":`)) {
		t.Fatal("audit events contain password material")
	}
}

func TestCustomerRejectsInvalidInputsBeforeWriting(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*CustomerInput)
	}{
		{"empty username", func(i *CustomerInput) { i.Username = " " }},
		{"username path", func(i *CustomerInput) { i.Username = "user/name" }},
		{"oversized username", func(i *CustomerInput) { i.Username = strings.Repeat("a", 65) }},
		{"invalid group", func(i *CustomerInput) { i.UserGroupID = "../other" }},
		{"empty password", func(i *CustomerInput) { i.Password = "" }},
		{"oversized password", func(i *CustomerInput) { i.Password = strings.Repeat("p", 1025) }},
		{"negative traffic", func(i *CustomerInput) { i.TrafficLimitBytes = -1 }},
		{"negative rules", func(i *CustomerInput) { i.MaxRules = -1 }},
		{"too many rules", func(i *CustomerInput) { i.MaxRules = 1_000_001 }},
		{"negative speed", func(i *CustomerInput) { i.SpeedLimitMbps = -1 }},
		{"negative ips", func(i *CustomerInput) { i.IPLimit = -1 }},
		{"negative connections", func(i *CustomerInput) { i.ConnectionLimit = -1 }},
		{"missing replay key", func(i *CustomerInput) { i.IdempotencyKey = "" }},
		{"control in replay key", func(i *CustomerInput) { i.IdempotencyKey = "1234567\n8" }},
		{"creation revision", func(i *CustomerInput) { i.Revision = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &recordingRepository{}
			input := validCustomerInput()
			test.change(&input)
			_, _, err := NewService(repo, nil).CreateCustomer(context.Background(), "admin", input)
			if !errors.Is(err, faults.ErrValidation) || len(repo.customerWrites) != 0 {
				t.Fatalf("err=%v writes=%d", err, len(repo.customerWrites))
			}
		})
	}
}

func TestUserGroupAuthorizationInputIsCanonicalAndBounded(t *testing.T) {
	repo := &recordingRepository{}
	service := NewService(repo, nil)
	input := UserGroupInput{Name: "  测试组  ", AllowedEntryGroupIDs: []string{"grp_z", "grp_a"}, IdempotencyKey: "create-group-1"}
	created, _, err := service.CreateUserGroup(context.Background(), "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "测试组" || created.AllowedEntryGroupIDs[0] != "grp_a" || created.AllowedExitGroupIDs == nil {
		t.Fatalf("unexpected group %#v", created)
	}
	input.AllowedEntryGroupIDs[0] = "grp_external-mutation"
	if created.AllowedEntryGroupIDs[1] != "grp_z" {
		t.Fatal("input slice shared with model")
	}
	for _, ids := range [][]string{{"grp_a", "grp_a"}, {"../bad"}, {""}, make([]string, 1001)} {
		input.AllowedEntryGroupIDs = ids
		_, _, err := service.CreateUserGroup(context.Background(), "admin", input)
		if !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("accepted invalid IDs: %v", err)
		}
	}
}

func TestPasswordFingerprintHasStableRetryAndDetectsChangedSecret(t *testing.T) {
	input := validCustomerInput()
	first, err := customerRequestFingerprint("admin", "", input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := customerRequestFingerprint("admin", "", input)
	if err != nil || first != again {
		t.Fatal("retry fingerprint changed")
	}
	input.Password = "2"
	changed, err := customerRequestFingerprint("admin", "", input)
	if err != nil || first == changed {
		t.Fatal("changed password reused old fingerprint")
	}
	input.Password = "1"
	input.IdempotencyKey = "another-create-key"
	other, err := customerRequestFingerprint("admin", "", input)
	if err != nil || first == other {
		t.Fatal("password fingerprint was not key scoped")
	}
}

func TestCustomerUpdateRevisionAndUTCExpiryBoundaries(t *testing.T) {
	for _, revision := range []int64{-1, 0, math.MaxInt64} {
		input := validCustomerInput()
		input.Revision = revision
		if _, err := normalizeCustomerInput(input, false); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("accepted revision %d", revision)
		}
	}
	for _, raw := range []string{"9999-12-31T23:59:59-01:00", "1970-01-01T00:00:00+01:00"} {
		expires, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Fatal(err)
		}
		input := validCustomerInput()
		input.ExpiresAt = &expires
		if _, err := normalizeCustomerInput(input, true); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("accepted UTC expiry out of supported range: %s", raw)
		}
	}
}
