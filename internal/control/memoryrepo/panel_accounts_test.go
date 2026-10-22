package memoryrepo

import (
	"encoding/json"
	"testing"

	"github.com/hongle/hl-panel/internal/control/customers"
)

// Fixtures must model the independent credential registry, never credentials
// on a service owner. Updating business fields keeps any existing login.
func seedCustomer(s *Store, c customers.Customer) {
	if len(c.PasswordHash) > 0 {
		s.panelAccounts[c.ID] = panelAccount{PasswordHash: append([]byte(nil), c.PasswordHash...)}
	}
	c.PasswordHash, c.AccountKind = nil, ""
	s.customers[c.ID] = cloneCustomer(c)
}

// Produce an actual pre-split document for legacy schema tests.
func legacyLoginSnapshot(t testing.TB, raw []byte) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var accounts map[string]panelAccount
	var owners map[string]storedCustomer
	if err := json.Unmarshal(doc["PanelAccounts"], &accounts); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(doc["Customers"], &owners); err != nil {
		t.Fatal(err)
	}
	for id, account := range accounts {
		owner := owners[id]
		owner.PasswordHash = account.PasswordHash
		owners[id] = owner
	}
	var err error
	doc["Customers"], err = json.Marshal(owners)
	if err != nil {
		t.Fatal(err)
	}
	delete(doc, "PanelAccounts")
	result, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func protectedServiceState(t testing.TB, raw []byte) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"PanelAccounts", "CustomerSessions", "AuditEvents", "BusinessIdempotency"} {
		delete(doc, key)
	}
	result, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
