package customers

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestStoredCustomerValidationDoesNotRequirePasswordAndRejectsCorruption(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	base := Customer{ID: "cus-valid", Username: "customer", DisplayName: "客户", UserGroupID: "ugrp-valid", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := ValidateStoredCustomer(base); err != nil {
		t.Fatalf("private hash must be validated separately: %v", err)
	}
	ungrouped := base
	ungrouped.UserGroupID = ""
	if err := ValidateStoredCustomer(ungrouped); err != nil {
		t.Fatalf("ungrouped customer must remain a valid account: %v", err)
	}
	maximum := base
	maximum.TrafficLimitBytes, maximum.TrafficUsedBytes, maximum.Revision = math.MaxInt64, math.MaxInt64, math.MaxInt64
	if err := ValidateStoredCustomer(maximum); err != nil {
		t.Fatalf("valid int64 boundary rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*Customer)
	}{
		{"invalid id", func(c *Customer) { c.ID = "../wrong" }},
		{"invalid group", func(c *Customer) { c.UserGroupID = "../wrong" }},
		{"noncanonical username", func(c *Customer) { c.Username = "Customer" }},
		{"empty username", func(c *Customer) { c.Username = "" }},
		{"negative quota", func(c *Customer) { c.TrafficLimitBytes = -1 }},
		{"negative accounting", func(c *Customer) { c.TrafficUsedBytes = -1 }},
		{"negative max rules", func(c *Customer) { c.MaxRules = -1 }},
		{"negative speed", func(c *Customer) { c.SpeedLimitMbps = -1 }},
		{"negative ip limit", func(c *Customer) { c.IPLimit = -1 }},
		{"negative connection limit", func(c *Customer) { c.ConnectionLimit = -1 }},
		{"oversized connection limit", func(c *Customer) { c.ConnectionLimit = 100_000_001 }},
		{"zero revision", func(c *Customer) { c.Revision = 0 }},
		{"missing created time", func(c *Customer) { c.CreatedAt = time.Time{} }},
		{"missing updated time", func(c *Customer) { c.UpdatedAt = time.Time{} }},
		{"backwards lifecycle", func(c *Customer) { c.UpdatedAt = now.Add(-time.Second) }},
		{"expired outside range", func(c *Customer) { bad := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC); c.ExpiresAt = &bad }},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := base
			test.change(&item)
			if err := ValidateStoredCustomer(item); !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("bad stored customer accepted: %v", err)
			}
		})
	}
}

func TestStoredUserGroupValidationRejectsInvalidAuthorizationAndTime(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	base := UserGroup{ID: "ugrp-valid", Name: "有效组", AllowedEntryGroupIDs: []string{"entry-valid"}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := ValidateStoredUserGroup(base); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*UserGroup)
	}{
		{"invalid id", func(g *UserGroup) { g.ID = "bad:id" }},
		{"empty name", func(g *UserGroup) { g.Name = "" }},
		{"noncanonical name", func(g *UserGroup) { g.Name = " group " }},
		{"duplicate entry", func(g *UserGroup) { g.AllowedEntryGroupIDs = []string{"entry-valid", "entry-valid"} }},
		{"invalid exit", func(g *UserGroup) { g.AllowedExitGroupIDs = []string{"bad/ref"} }},
		{"noncanonical reference", func(g *UserGroup) { g.AllowedEntryGroupIDs = []string{" entry-valid "} }},
		{"negative revision", func(g *UserGroup) { g.Revision = -1 }},
		{"missing time", func(g *UserGroup) { g.CreatedAt = time.Time{} }},
		{"backwards lifecycle", func(g *UserGroup) { g.UpdatedAt = now.Add(-time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := base
			test.change(&item)
			if err := ValidateStoredUserGroup(item); !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("bad stored user group accepted: %v", err)
			}
		})
	}
}
