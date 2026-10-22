package memoryrepo

import "github.com/hongle/hl-panel/internal/control/customers"

// A panel account grants login access to an existing service owner. Its
// lifecycle is deliberately independent of rules, subscriptions and quotas.
type panelAccount struct {
	PasswordHash []byte
}

func (s *Store) customerWithLoginLocked(item customers.Customer) customers.Customer {
	item = cloneCustomer(item)
	item.AccountKind = customers.AccountService
	item.PasswordHash = nil
	if account, exists := s.panelAccounts[item.ID]; exists {
		item.AccountKind = customers.AccountPanel
		item.PasswordHash = append([]byte(nil), account.PasswordHash...)
	}
	return item
}
