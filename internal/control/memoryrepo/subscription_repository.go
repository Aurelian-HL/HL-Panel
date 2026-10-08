package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/securetoken"
	"reflect"
	"sort"
)

var _ subscriptions.Repository = (*Store)(nil)

func cloneSubscription(r subscriptions.Record) subscriptions.Record {
	r.Draft = append([]subscriptions.Line(nil), r.Draft...)
	r.Published = append([]subscriptions.Line(nil), r.Published...)
	return r
}

// Names are a read projection of the current rule. Renaming a rule never
// rotates its subscription token or changes the stored publication revision.
func (s *Store) subscriptionNamesLocked(r subscriptions.Record) subscriptions.Record {
	r = cloneSubscription(r)
	if rule, ok := s.forwardRules[r.Item.ForwardingRuleID]; ok && rule.OwnedByAdministrator(r.OwnerID) {
		r.Item.Name = rule.Name
		for i := range r.Draft {
			r.Draft[i].Name = rule.Name
		}
		for i := range r.Published {
			r.Published[i].Name = rule.Name
		}
	}
	return r
}
func (s *Store) ListSubscriptions(_ context.Context, admin string) ([]subscriptions.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []subscriptions.Item{}
	for _, r := range s.subscriptions {
		if r.OwnerID == admin {
			items = append(items, s.subscriptionNamesLocked(r).Item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}
func (s *Store) Subscription(_ context.Context, admin, id string) (subscriptions.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.subscriptions[id]
	if !ok || r.OwnerID != admin {
		return subscriptions.Record{}, faults.ErrNotFound
	}
	return s.subscriptionNamesLocked(r), nil
}
func (s *Store) SubscriptionByToken(_ context.Context, hash string) (subscriptions.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.subscriptions {
		if securetoken.Hash(r.Token) == hash {
			return s.subscriptionNamesLocked(r), nil
		}
	}
	return subscriptions.Record{}, faults.ErrNotFound
}
func (s *Store) MutateSubscription(_ context.Context, c subscriptions.Command, event audit.Event) (subscriptions.Item, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := c.AdministratorID + "\x00subscription." + c.Operation + "\x00"
	if c.Operation != "create" && c.Operation != "generate" {
		key += c.ID + "\x00"
	}
	key += c.IdempotencyKey
	var replay subscriptions.Item
	if ok, err := s.replayBusinessLocked(key, c.RequestSHA256, &replay); ok || err != nil {
		return replay, ok, err
	}
	if c.Operation == "generate" {
		rule, ok := s.forwardRules[c.ForwardingRuleID]
		if !ok || !rule.OwnedByAdministrator(c.AdministratorID) || rule.CustomerID != c.Request.CustomerID || rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || len(c.Request.Lines) != 1 {
			return subscriptions.Item{}, false, faults.ErrNotFound
		}
		binding, ok := s.vlessBindings[c.Request.Lines[0].BindingID]
		if !ok || binding.Binding.ForwardingRuleID != rule.ID || binding.Binding.CustomerID != rule.CustomerID || !s.vlessBindingOwnedByAdministratorLocked(binding.Binding, c.AdministratorID) || binding.Binding.State != vlessidentity.StateActive {
			return subscriptions.Item{}, false, faults.ErrNotFound
		}
		for _, r := range s.subscriptions {
			if r.OwnerID == c.AdministratorID && r.Item.ForwardingRuleID == c.ForwardingRuleID {
				if r.Item.State == "revoked" {
					return subscriptions.Item{}, false, faults.ErrConflict
				}
				if r.Item.Name == c.Request.Name && r.Item.CustomerID == c.Request.CustomerID && reflect.DeepEqual(r.Draft, c.Request.Lines) {
					return r.Item, true, nil
				}
				c.ID = r.Item.ID
				c.Request.Revision = r.Item.Revision
				break
			}
		}
	}
	previous, exists := s.subscriptions[c.ID]
	if c.Operation == "create" || (c.Operation == "generate" && !exists) {
		if exists {
			return subscriptions.Item{}, false, faults.ErrConflict
		}
		found := false
		for _, a := range s.adminsByUsername {
			found = found || a.ID == c.AdministratorID
		}
		if !found {
			return subscriptions.Item{}, false, faults.ErrNotFound
		}
	} else if !exists || previous.OwnerID != c.AdministratorID {
		return subscriptions.Item{}, false, faults.ErrNotFound
	}
	if exists && previous.Item.ForwardingRuleID != "" && c.Operation == "update" {
		return subscriptions.Item{}, false, faults.ErrValidation
	}
	if exists && c.Operation == "update" && c.Request.CustomerID != previous.Item.CustomerID {
		return subscriptions.Item{}, false, faults.ErrValidation
	}
	next, err := subscriptions.Apply(previous, c)
	if err != nil {
		return subscriptions.Item{}, false, err
	}
	if _, ok := s.customers[next.Item.CustomerID]; !ok && !(next.Item.ForwardingRuleID != "" && next.Item.CustomerID == forwarding.AdministratorSubjectID(c.AdministratorID)) {
		return subscriptions.Item{}, false, faults.ErrNotFound
	}
	if c.Operation == "create" || c.Operation == "generate" || c.Operation == "update" || c.Operation == "publish" {
		for _, line := range next.Draft {
			if line.BindingID == "" {
				continue
			}
			binding, ok := s.vlessBindings[line.BindingID]
			if !ok || binding.Binding.State != vlessidentity.StateActive || (binding.Binding.CustomerID != next.Item.CustomerID && binding.Binding.CustomerID != forwarding.AdministratorSubjectID(c.AdministratorID)) || !s.vlessBindingOwnedByAdministratorLocked(binding.Binding, c.AdministratorID) {
				return subscriptions.Item{}, false, faults.ErrNotFound
			}
		}
	}
	for id, r := range s.subscriptions {
		if id != next.Item.ID && r.Token == next.Token {
			return subscriptions.Item{}, false, faults.ErrConflict
		}
	}
	if err := s.recordBusinessLocked(key, c.RequestSHA256, next.Item.ID, next.Item); err != nil {
		return subscriptions.Item{}, false, err
	}
	s.subscriptions[next.Item.ID] = cloneSubscription(next)
	event.ResourceID = next.Item.ID
	s.appendAuditLocked(event)
	return next.Item, false, nil
}
