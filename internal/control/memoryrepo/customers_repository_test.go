package memoryrepo

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
)

func customerRepositoryFixture(t *testing.T) (*Store, customers.Customer) {
	t.Helper()
	hash, err := auth.HashPassword("customer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	s := New(auth.Administrator{ID: "admin-test", Username: "admin", PasswordHash: hash})
	s.deviceGroups["entry-1"] = groups.DeviceGroup{ID: "entry-1", Name: "entry", Kind: groups.KindEntry}
	s.deviceGroups["exit-1"] = groups.DeviceGroup{ID: "exit-1", Name: "exit", Kind: groups.KindExit}
	for _, id := range []string{"entry-1", "exit-1"} {
		s.membersByGroup[id] = map[string]groups.Member{}
		s.revisionByIdempotency[id] = map[string]string{}
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	s.userGroups["ugrp-1"] = customers.UserGroup{ID: "ugrp-1", Name: "allowed", AllowedEntryGroupIDs: []string{"entry-1"}, AllowedExitGroupIDs: []string{"exit-1"}, AllowDirect: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	return s, customers.Customer{ID: "cus-1", Username: "customer", UserGroupID: "ugrp-1", PasswordHash: hash, Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func customerSaveInput(item customers.Customer, key string) customers.SaveCustomerInput {
	return customers.SaveCustomerInput{Customer: item, ExpectedRevision: item.Revision - 1, IdempotencyKey: key, RequestSHA256: key + "-hash", CreatedBy: "admin-test"}
}

func TestUngroupedCustomerCanExistButCannotCreateRule(t *testing.T) {
	ctx := context.Background()
	s, item := customerRepositoryFixture(t)
	item.UserGroupID = ""
	if _, _, err := s.SaveCustomer(ctx, customerSaveInput(item, "create-ungrouped"), audit.Event{}); err != nil {
		t.Fatal(err)
	}
	record, err := s.CustomerIdentityRepository("test").CustomerByUsername(ctx, item.Username)
	if err != nil || record.UserGroupName != "未分组" {
		t.Fatalf("ungrouped customer record = %+v, err = %v", record, err)
	}
	options, err := s.CustomerIdentityRepository("test").RuleOptionsByCustomer(ctx, item.ID)
	if err != nil || len(options.EntryGroups) != 0 || len(options.ExitGroups) != 0 {
		t.Fatalf("ungrouped customer has rule authorization: %+v, err = %v", options, err)
	}
	request := forwarding.Request{Name: "blocked", CustomerID: item.ID, EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect,
		Protocol: forwarding.ProtocolTCP, Targets: []forwarding.Target{{Host: "example.test", Port: 443}}, SelectionPolicy: forwarding.SelectionRoundRobin}
	if _, _, err := forwarding.NewService(s, nil).Create(ctx, "admin-test", request, "ungrouped-rule"); !errors.Is(err, faults.ErrValidation) || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("ungrouped rule unexpectedly authorized: %v", err)
	}
	encoded, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(encoded); err != nil {
		t.Fatalf("ungrouped customer snapshot rejected: %v", err)
	}
}

func TestCustomerRepositoryReplayRevisionUniquenessAndCloning(t *testing.T) {
	ctx := context.Background()
	s, item := customerRepositoryFixture(t)
	expires := item.CreatedAt.Add(time.Hour)
	item.ExpiresAt = &expires
	input := customerSaveInput(item, "create-customer")
	got, replayed, err := s.SaveCustomer(ctx, input, audit.Event{Action: "create"})
	if err != nil || replayed || got.ID != item.ID {
		t.Fatalf("create: %v", err)
	}
	got.PasswordHash[0] = '!'
	*got.ExpiresAt = time.Time{}
	stored, err := s.Customer(ctx, item.ID)
	if err != nil || !bytes.Equal(stored.PasswordHash, item.PasswordHash) || !stored.ExpiresAt.Equal(expires) {
		t.Fatal("read shared mutable fields with store")
	}
	replay, replayed, err := s.SaveCustomer(ctx, input, audit.Event{Action: "replayed"})
	if err != nil || !replayed || replay.ID != item.ID || len(s.AuditEvents()) != 1 {
		t.Fatalf("replay appended audit or failed: %v", err)
	}
	conflict := input
	conflict.RequestSHA256 = "other"
	if _, _, err = s.SaveCustomer(ctx, conflict, audit.Event{}); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("changed payload: %v", err)
	}
	duplicate := item
	duplicate.ID = "cus-other"
	duplicate.Username = "CUSTOMER"
	if _, _, err = s.SaveCustomer(ctx, customerSaveInput(duplicate, "duplicate-user"), audit.Event{}); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("duplicate username: %v", err)
	}
	item.Revision = 2
	item.DisplayName = "updated"
	item.TrafficUsedBytes = 999
	update := customerSaveInput(item, "update-customer")
	updated, _, err := s.SaveCustomer(ctx, update, audit.Event{Action: "update"})
	if err != nil || updated.Revision != 2 || updated.TrafficUsedBytes != 0 {
		t.Fatalf("update lost invariants: %v", err)
	}
	if _, replayed, err = s.SaveCustomer(ctx, update, audit.Event{}); err != nil || !replayed {
		t.Fatalf("PUT replay failed after revision advanced: %v", err)
	}
	update.IdempotencyKey = "stale-update"
	update.RequestSHA256 = "stale"
	if _, _, err = s.SaveCustomer(ctx, update, audit.Event{}); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if len(s.AuditEvents()) != 2 {
		t.Fatal("rejected writes left audit records")
	}
}

func TestConcurrentCustomerRevisionAllowsExactlyOneWinner(t *testing.T) {
	s, item := customerRepositoryFixture(t)
	ctx := context.Background()
	if _, _, err := s.SaveCustomer(ctx, customerSaveInput(item, "create"), audit.Event{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"writer-one", "writer-two"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			updated := item
			updated.Revision = 2
			updated.DisplayName = key
			_, _, err := s.SaveCustomer(ctx, customerSaveInput(updated, key), audit.Event{})
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, faults.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestBlankPasswordEditPreservesConcurrentPasswordReset(t *testing.T) {
	s, item := customerRepositoryFixture(t)
	ctx := context.Background()
	if _, _, err := s.SaveCustomer(ctx, customerSaveInput(item, "create-before-reset"), audit.Event{}); err != nil {
		t.Fatal(err)
	}
	staleEdit := item
	newHash, err := auth.HashPassword("new-password-after-read")
	if err != nil {
		t.Fatal(err)
	}
	reset := item
	reset.PasswordHash, reset.Revision = newHash, 2
	resetInput := customerSaveInput(reset, "explicit-password-reset")
	resetInput.PasswordChanged = true
	if _, _, err := s.SaveCustomer(ctx, resetInput, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	// An edit can read revision 1 before a concurrent reset and submit a
	// revision 2 update afterward. Its blank password must preserve the hash
	// in the transaction rather than the stale value carried by its read.
	staleEdit.Revision, staleEdit.DisplayName = 3, "edited after reset"
	updated, _, err := s.SaveCustomer(ctx, customerSaveInput(staleEdit, "blank-password-edit"), audit.Event{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(updated.PasswordHash, newHash) {
		t.Fatal("blank-password edit reverted the concurrent reset")
	}
}

func TestCustomerRepositoryRejectsRevisionOverflowAndMissingMutationIdentity(t *testing.T) {
	s, item := customerRepositoryFixture(t)
	item.Revision = math.MaxInt64
	s.customers[item.ID] = item
	wrapped := item
	wrapped.Revision = math.MinInt64
	input := customerSaveInput(item, "overflow-update")
	input.Customer, input.ExpectedRevision = wrapped, math.MaxInt64
	if _, _, err := s.SaveCustomer(context.Background(), input, audit.Event{}); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("overflow revision accepted: %v", err)
	}
	input = customerSaveInput(item, "missing-actor")
	input.CreatedBy = ""
	if _, _, err := s.SaveCustomer(context.Background(), input, audit.Event{}); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("missing actor accepted: %v", err)
	}
	group := s.userGroups["ugrp-1"]
	group.Revision = math.MaxInt64
	s.userGroups[group.ID] = group
	group.Revision = math.MinInt64
	groupInput := customers.SaveUserGroupInput{UserGroup: group, ExpectedRevision: math.MaxInt64, IdempotencyKey: "overflow-group", RequestSHA256: "overflow-group-hash", CreatedBy: "admin"}
	if _, _, err := s.SaveUserGroup(context.Background(), groupInput, audit.Event{}); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("user group overflow accepted: %v", err)
	}
	if len(s.AuditEvents()) != 0 || len(s.businessIdempotency) != 0 {
		t.Fatal("invalid mutation wrote state")
	}
}

func TestUserGroupReferencesAndExistingRuleAuthorizationsAreTransactional(t *testing.T) {
	ctx := context.Background()
	s, item := customerRepositoryFixture(t)
	s.customers[item.ID] = item
	s.forwardRules["rule-1"] = forwarding.Rule{ID: "rule-1", CustomerID: item.ID, EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect, Paused: true}
	group := s.userGroups["ugrp-1"]
	group.Revision = 2
	group.AllowDirect = false
	input := customers.SaveUserGroupInput{UserGroup: group, ExpectedRevision: 1, CreatedBy: "admin-test", IdempotencyKey: "revoke-direct", RequestSHA256: "revoke"}
	if _, _, err := s.SaveUserGroup(ctx, input, audit.Event{}); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("revoked paused rule access: %v", err)
	}
	if !s.userGroups["ugrp-1"].AllowDirect || len(s.businessIdempotency) != 0 || len(s.AuditEvents()) != 0 {
		t.Fatal("failed authorization update changed state")
	}
	newGroup := customers.UserGroup{ID: "ugrp-2", Name: "invalid refs", AllowedEntryGroupIDs: []string{"missing"}, Revision: 1, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
	newInput := customers.SaveUserGroupInput{UserGroup: newGroup, CreatedBy: "admin-test", IdempotencyKey: "new-group", RequestSHA256: "new-group"}
	if _, _, err := s.SaveUserGroup(ctx, newInput, audit.Event{}); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("missing group: %v", err)
	}
	newInput.UserGroup.AllowedEntryGroupIDs = []string{"exit-1"}
	if _, _, err := s.SaveUserGroup(ctx, newInput, audit.Event{}); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("wrong group role: %v", err)
	}
	newInput.UserGroup.AllowedEntryGroupIDs = []string{}
	if _, _, err := s.SaveUserGroup(ctx, newInput, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	item.Revision = 2
	item.UserGroupID = "ugrp-2"
	if _, _, err := s.SaveCustomer(ctx, customerSaveInput(item, "change-group"), audit.Event{}); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("customer moved to unauthorized group: %v", err)
	}
	item.UserGroupID = "ugrp-1"
	item.MaxRules = 1
	s.forwardRules["rule-2"] = forwarding.Rule{ID: "rule-2", CustomerID: item.ID, EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect}
	if _, _, err := s.SaveCustomer(ctx, customerSaveInput(item, "lower-quota"), audit.Event{}); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("rule quota below existing count: %v", err)
	}
	item.UserGroupID = "unknown"
	if _, _, err := s.SaveCustomer(ctx, customerSaveInput(item, "missing-group"), audit.Event{}); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("unknown customer group: %v", err)
	}
}
