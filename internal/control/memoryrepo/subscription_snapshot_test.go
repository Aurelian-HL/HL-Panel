package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"testing"
	"time"
)

func TestSubscriptionSnapshotUpgradeRoundtripAndCorruption(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	hash, err := auth.HashPassword("isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	store := New(auth.Administrator{ID: "subscription-admin", Username: "admin", PasswordHash: hash, CreatedAt: now})
	old, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	json.Unmarshal(old, &legacy)
	legacy["Version"] = json.RawMessage("16")
	delete(legacy, "Subscriptions")
	old, _ = json.Marshal(legacy)
	store, err = DecodeSnapshot(old)
	if err != nil {
		t.Fatal("v16 upgrade", err)
	}
	customer, _, err := customers.NewService(store, nil).CreateCustomer(ctx, "subscription-admin", customers.CustomerInput{Username: "subscriber", Password: "123456", IdempotencyKey: "customer-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	service := subscriptions.NewService(store, nil, nil)
	request := subscriptions.Request{Name: "Subscription", CustomerID: customer.ID, Lines: []subscriptions.Line{{Name: "line", URI: "socks5://user:secret@edge.example.test:1080"}}}
	item, _, err := service.Mutate(ctx, "subscription-admin", "", "create", request, "create-subscription")
	if err != nil {
		t.Fatal(err)
	}
	item, _, err = service.Mutate(ctx, "subscription-admin", item.ID, "publish", subscriptions.Request{Revision: item.Revision}, "publish-subscription")
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Detail(ctx, "subscription-admin", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restored.EncodeSnapshot()
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("subscription snapshot lost state")
	}
	reopened := subscriptions.NewService(restored, nil, nil)
	if _, _, err = reopened.Public(ctx, record.Token); err != nil {
		t.Fatal("published feed lost after restore")
	}
	replay, replayed, err := reopened.Mutate(ctx, "subscription-admin", "", "create", request, "create-subscription")
	if err != nil || !replayed || replay.ID != item.ID {
		t.Fatal("replay lost after restart")
	}
	var state snapshot
	json.Unmarshal(raw, &state)
	for id, r := range state.Subscriptions {
		r.OwnerID = "missing-admin"
		state.Subscriptions[id] = r
	}
	bad, _ := json.Marshal(state)
	if _, err = DecodeSnapshot(bad); err == nil {
		t.Fatal("invalid owner accepted")
	}
	json.Unmarshal(raw, &state)
	for id, r := range state.Subscriptions {
		r.Published[0].Name = "mismatch"
		state.Subscriptions[id] = r
	}
	bad, _ = json.Marshal(state)
	if _, err = DecodeSnapshot(bad); err == nil {
		t.Fatal("published metadata mismatch accepted")
	}
	// Customer restrictions are checked at every public fetch, including saved manual links.
	expired := now.Add(-time.Hour)
	for _, change := range []func(*customers.Customer){func(c *customers.Customer) { c.Disabled = true }, func(c *customers.Customer) { c.ExpiresAt = &expired }, func(c *customers.Customer) { c.TrafficLimitBytes = 1; c.TrafficUsedBytes = 1 }} {
		c := customer
		change(&c)
		restored.customers[c.ID] = c
		if _, _, err = reopened.Public(ctx, record.Token); err == nil {
			t.Fatal("restricted customer feed exposed")
		}
	}
}
