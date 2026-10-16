package memoryrepo

import "github.com/hongle/hl-panel/internal/control/forwarding"

// The exception is issued only by group deletion for an existing listener.
// It cannot grant a new rule, another customer, port, protocol or route.
func (s *Store) detachedRuleAuthorizedLocked(rule forwarding.Rule) bool {
	previous, exists := s.forwardRules[rule.ID]
	customer, customerExists := s.customers[rule.CustomerID]
	return exists && customerExists && customer.UserGroupID == "" && previous.UserGroupDetached && previous.OwnedByCustomer(rule.CustomerID) &&
		previous.EntryGroupID == rule.EntryGroupID && previous.ExitGroupID == rule.ExitGroupID && previous.EgressMode == rule.EgressMode &&
		previous.ListenPort == rule.ListenPort && previous.EffectiveIngressProtocol() == rule.EffectiveIngressProtocol()
}
