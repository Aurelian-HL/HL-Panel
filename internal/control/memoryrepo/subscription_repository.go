package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/securetoken"
	"sort"
)

var _ subscriptions.Repository = (*Store)(nil)

func cloneSubscription(r subscriptions.Record) subscriptions.Record {
	r.Draft = append([]subscriptions.Line(nil), r.Draft...)
	r.Published = append([]subscriptions.Line(nil), r.Published...)
	return r
}
func (s *Store) ListSubscriptions(_ context.Context, admin string) ([]subscriptions.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []subscriptions.Item{}
	for _, r := range s.subscriptions {
		if r.OwnerID == admin {
			items = append(items, r.Item)
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
	return cloneSubscription(r), nil
}
func (s *Store) SubscriptionByToken(_ context.Context, hash string) (subscriptions.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.subscriptions {
		if securetoken.Hash(r.Token) == hash {
			return cloneSubscription(r), nil
		}
	}
	return subscriptions.Record{}, faults.ErrNotFound
}
func (s *Store) MutateSubscription(_ context.Context, c subscriptions.Command, event audit.Event) (subscriptions.Item, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := c.AdministratorID + "\x00subscription." + c.Operation + "\x00"
	if c.Operation != "create" {
		key += c.ID + "\x00"
	}
	key += c.IdempotencyKey
	var replay subscriptions.Item
	if ok, err := s.replayBusinessLocked(key, c.RequestSHA256, &replay); ok || err != nil {
		return replay, ok, err
	}
	previous, exists := s.subscriptions[c.ID]
	if c.Operation == "create" {
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
	if exists && c.Operation == "update" && c.Request.CustomerID != previous.Item.CustomerID {
		return subscriptions.Item{}, false, faults.ErrValidation
	}
	next, err := subscriptions.Apply(previous, c)
	if err != nil {
		return subscriptions.Item{}, false, err
	}
	if _, ok := s.customers[next.Item.CustomerID]; !ok {
		return subscriptions.Item{}, false, faults.ErrNotFound
	}
	if c.Operation == "create" || c.Operation == "update" || c.Operation == "publish" {
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
	s.appendAuditLocked(event)
	return next.Item, false, nil
}
