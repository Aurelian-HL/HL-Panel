package memoryrepo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

type customerIdentityRepository struct {
	store        *Store
	panelVersion string
}

var _ customeridentity.Repository = (*customerIdentityRepository)(nil)

// CustomerIdentityRepository returns a customer-only view over the control
// store. Its sessions are physically separate from administrator sessions.
func (s *Store) CustomerIdentityRepository(panelVersion string) customeridentity.Repository {
	return &customerIdentityRepository{store: s, panelVersion: strings.TrimSpace(panelVersion)}
}

func (r *customerIdentityRepository) CustomerByUsername(_ context.Context, username string) (customeridentity.CustomerRecord, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	for _, item := range r.store.customers {
		if strings.EqualFold(item.Username, username) {
			return r.customerRecordLocked(item)
		}
	}
	return customeridentity.CustomerRecord{}, faults.ErrNotFound
}

func (r *customerIdentityRepository) CustomerByID(_ context.Context, id string) (customeridentity.CustomerRecord, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	item, exists := r.store.customers[id]
	if !exists {
		return customeridentity.CustomerRecord{}, faults.ErrNotFound
	}
	return r.customerRecordLocked(item)
}

func (r *customerIdentityRepository) customerRecordLocked(item customers.Customer) (customeridentity.CustomerRecord, error) {
	if item.UserGroupID == "" {
		return customeridentity.CustomerRecord{Customer: cloneCustomer(item), UserGroupName: "未分组"}, nil
	}
	group, exists := r.store.userGroups[item.UserGroupID]
	if !exists {
		return customeridentity.CustomerRecord{}, errors.New("customer references a missing user group")
	}
	return customeridentity.CustomerRecord{Customer: cloneCustomer(item), UserGroupName: group.Name}, nil
}

func (r *customerIdentityRepository) CreateSession(_ context.Context, session customeridentity.Session, event audit.Event) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if _, exists := r.store.customers[session.CustomerID]; !exists {
		return faults.ErrNotFound
	}
	if _, exists := r.store.customerSessions[session.TokenHash]; exists {
		return faults.ErrConflict
	}
	r.store.customerSessions[session.TokenHash] = session
	r.store.appendAuditLocked(event)
	return nil
}

func (r *customerIdentityRepository) SessionByTokenHash(_ context.Context, hash string, now time.Time) (customeridentity.Session, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	session, exists := r.store.customerSessions[hash]
	if !exists || !now.Before(session.ExpiresAt) {
		return customeridentity.Session{}, faults.ErrUnauthorized
	}
	return session, nil
}

func (r *customerIdentityRepository) RevokeSession(_ context.Context, hash, customerID string, event audit.Event) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	session, exists := r.store.customerSessions[hash]
	if !exists || session.CustomerID != customerID {
		return faults.ErrUnauthorized
	}
	delete(r.store.customerSessions, hash)
	r.store.appendAuditLocked(event)
	return nil
}

func passwordChangeKey(customerID, idempotencyKey string) string {
	return customerID + "\x00customer.password_change\x00" + idempotencyKey
}

func (r *customerIdentityRepository) PasswordChangeReplay(_ context.Context, customerID, idempotencyKey, fingerprint string) (bool, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	previous, exists := r.store.businessIdempotency[passwordChangeKey(customerID, idempotencyKey)]
	if !exists {
		return false, nil
	}
	if previous.RequestSHA256 != fingerprint {
		return false, faults.ErrIdempotencyConflict
	}
	return true, nil
}

func (r *customerIdentityRepository) ChangePassword(_ context.Context, input customeridentity.ChangePasswordInput, event audit.Event) (bool, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	key := passwordChangeKey(input.CustomerID, input.IdempotencyKey)
	if previous, exists := r.store.businessIdempotency[key]; exists {
		if previous.RequestSHA256 != input.RequestHMACSHA256 {
			return false, faults.ErrIdempotencyConflict
		}
		return true, nil
	}
	currentSession, exists := r.store.customerSessions[input.KeepSessionHash]
	if !exists || currentSession.CustomerID != input.CustomerID {
		return false, faults.ErrUnauthorized
	}
	item, exists := r.store.customers[input.CustomerID]
	if !exists {
		return false, faults.ErrNotFound
	}
	if item.Revision != input.ExpectedRevision || item.Revision == int64(^uint64(0)>>1) {
		return false, fmt.Errorf("%w: customer changed; reload before changing password", faults.ErrConflict)
	}
	if auth.ValidatePasswordHash(input.PasswordHash) != nil || input.UpdatedAt.IsZero() {
		return false, fmt.Errorf("%w: invalid password mutation", faults.ErrValidation)
	}
	item.PasswordHash = append([]byte(nil), input.PasswordHash...)
	item.Revision++
	item.UpdatedAt = input.UpdatedAt.UTC()
	r.store.customers[item.ID] = item
	for hash, session := range r.store.customerSessions {
		if session.CustomerID == input.CustomerID && hash != input.KeepSessionHash {
			delete(r.store.customerSessions, hash)
		}
	}
	if err := r.store.recordBusinessLocked(key, input.RequestHMACSHA256, input.CustomerID, true); err != nil {
		return false, err
	}
	r.store.appendAuditLocked(event)
	return false, nil
}

func (r *customerIdentityRepository) ListRulesByCustomer(_ context.Context, customerID string) ([]customeridentity.RuleRecord, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	if _, exists := r.store.customers[customerID]; !exists {
		return nil, faults.ErrNotFound
	}
	items := make([]customeridentity.RuleRecord, 0)
	for _, stored := range r.store.forwardRules {
		if stored.CustomerID != customerID {
			continue
		}
		rule := r.store.forwardingViewLocked(stored)
		network, exists := r.store.groupNetworks[rule.EntryGroupID]
		if !exists {
			return nil, errors.New("customer rule references an unconfigured entry group")
		}
		targets := make([]customeridentity.TargetView, len(rule.Targets))
		for index, target := range rule.Targets {
			targets[index] = customeridentity.TargetView{Host: target.Host, Port: target.Port}
		}
		items = append(items, customeridentity.RuleRecord{
			CustomerID: customerID, ID: rule.ID, Name: rule.Name, IngressProtocol: string(rule.EffectiveIngressProtocol()), Protocol: string(rule.Protocol),
			ConnectHost: network.ConnectHost, ListenPort: rule.ListenPort,
			RouteDescription: r.routeDescriptionLocked(rule), Targets: targets, Paused: rule.Paused,
			Status: string(rule.Status), Deployed: rule.Deployed, Revision: rule.Revision,
		})
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Name == items[right].Name {
			return items[left].ID < items[right].ID
		}
		return items[left].Name < items[right].Name
	})
	return items, nil
}

func (r *customerIdentityRepository) RuleOptionsByCustomer(_ context.Context, customerID string) (customeridentity.RuleOptionsView, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	customer, exists := r.store.customers[customerID]
	if !exists {
		return customeridentity.RuleOptionsView{}, faults.ErrNotFound
	}
	userGroup, exists := r.store.userGroups[customer.UserGroupID]
	if customer.UserGroupID == "" {
		return customeridentity.RuleOptionsView{
			EntryGroups: []customeridentity.EntryGroupOption{},
			ExitGroups:  []customeridentity.RuleGroupOption{},
			RuleGroups:  []customeridentity.RuleGroupOption{},
		}, nil
	}
	if !exists {
		return customeridentity.RuleOptionsView{}, faults.ErrNotFound
	}
	options := customeridentity.RuleOptionsView{
		EntryGroups: []customeridentity.EntryGroupOption{},
		ExitGroups:  []customeridentity.RuleGroupOption{},
		RuleGroups:  []customeridentity.RuleGroupOption{},
	}
	availableExits := make(map[string]string)
	for _, entryID := range userGroup.AllowedEntryGroupIDs {
		entry, ok := r.store.deviceGroups[entryID]
		network, configured := r.store.groupNetworks[entryID]
		if !ok || entry.Kind != groups.KindEntry || !configured || network.ConnectHost == "" || len(network.EffectivePortRanges()) == 0 {
			continue
		}
		if len(network.AllowedUserGroupIDs) > 0 && !slices.Contains(network.AllowedUserGroupIDs, customer.UserGroupID) {
			continue
		}
		option := customeridentity.EntryGroupOption{
			ID: entry.ID, Name: entry.Name, ConnectHost: network.ConnectHost,
			PortRanges:          []customeridentity.PortRangeOption{},
			AllowDirect:         userGroup.AllowDirect && network.EffectiveDirectPolicy().AllowsDirect(),
			AllowedExitGroupIDs: []string{},
		}
		for _, portRange := range network.EffectivePortRanges() {
			option.PortRanges = append(option.PortRanges, customeridentity.PortRangeOption{Start: portRange.Start, End: portRange.End})
		}
		if network.EffectiveDirectPolicy().AllowsExitGroup() {
			for _, exitID := range userGroup.AllowedExitGroupIDs {
				exit, ok := r.store.deviceGroups[exitID]
				if !ok || exit.Kind != groups.KindExit || exitID == entryID || !slices.Contains(network.AllowedExitGroupIDs, exitID) {
					continue
				}
				if exitNetwork, configured := r.store.groupNetworks[exitID]; configured {
					if len(exitNetwork.AllowedEntryGroupIDs) > 0 && !slices.Contains(exitNetwork.AllowedEntryGroupIDs, entryID) || len(exitNetwork.AllowedUserGroupIDs) > 0 && !slices.Contains(exitNetwork.AllowedUserGroupIDs, customer.UserGroupID) {
						continue
					}
				}
				option.AllowedExitGroupIDs = append(option.AllowedExitGroupIDs, exitID)
				availableExits[exitID] = exit.Name
			}
		}
		if !option.AllowDirect && len(option.AllowedExitGroupIDs) == 0 {
			continue
		}
		sort.Strings(option.AllowedExitGroupIDs)
		options.EntryGroups = append(options.EntryGroups, option)
	}
	for id, name := range availableExits {
		options.ExitGroups = append(options.ExitGroups, customeridentity.RuleGroupOption{ID: id, Name: name})
	}
	for _, group := range r.store.ruleGroups {
		options.RuleGroups = append(options.RuleGroups, customeridentity.RuleGroupOption{ID: group.ID, Name: group.Name})
	}
	sort.Slice(options.EntryGroups, func(i, j int) bool { return options.EntryGroups[i].Name < options.EntryGroups[j].Name })
	sort.Slice(options.ExitGroups, func(i, j int) bool { return options.ExitGroups[i].Name < options.ExitGroups[j].Name })
	sort.Slice(options.RuleGroups, func(i, j int) bool { return options.RuleGroups[i].Name < options.RuleGroups[j].Name })
	return options, nil
}

func (r *customerIdentityRepository) routeDescriptionLocked(rule forwarding.Rule) string {
	entryName := "入口"
	if group, exists := r.store.deviceGroups[rule.EntryGroupID]; exists && strings.TrimSpace(group.Name) != "" {
		entryName = group.Name
	}
	if rule.EgressMode == forwarding.EgressDirect {
		return entryName + " · 直出"
	}
	exitName := "出口"
	if group, exists := r.store.deviceGroups[rule.ExitGroupID]; exists && strings.TrimSpace(group.Name) != "" {
		exitName = group.Name
	}
	return entryName + " → " + exitName
}

func (r *customerIdentityRepository) CustomerUsage(_ context.Context, customerID string) (customeridentity.UsageRecord, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	item, exists := r.store.customers[customerID]
	if !exists {
		return customeridentity.UsageRecord{}, faults.ErrNotFound
	}
	return customeridentity.UsageRecord{CustomerID: customerID, TrafficUsedBytes: item.TrafficUsedBytes, TrafficLimitBytes: item.TrafficLimitBytes, UpdatedAt: item.UpdatedAt}, nil
}

func (r *customerIdentityRepository) ListSubscriptionsByCustomer(_ context.Context, customerID string) ([]customeridentity.SubscriptionRecord, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	if _, exists := r.store.customers[customerID]; !exists {
		return nil, faults.ErrNotFound
	}
	activeBindings := make(map[string]vlessidentity.CredentialRecord)
	for _, binding := range r.store.vlessBindings {
		if binding.Binding.CustomerID != customerID || binding.Binding.State != vlessidentity.StateActive || binding.CredentialUUID == "" {
			continue
		}
		activeBindings[binding.Binding.ForwardingRuleID+"\x00"+binding.Binding.EndpointPoolID] = cloneVLESSCredentialRecord(binding)
	}
	items := make([]customeridentity.SubscriptionRecord, 0)
	for _, stored := range r.store.forwardRules {
		if stored.CustomerID != customerID {
			continue
		}
		rule := r.store.forwardingViewLocked(stored)
		for _, pool := range r.store.endpointPools {
			if pool.GroupID != rule.EntryGroupID || (pool.RuleID != "" && pool.RuleID != rule.ID) || pool.Protocol != "vless" {
				continue
			}
			status := "identity_binding_required"
			if rule.Status != forwarding.StatusPendingActivation {
				status = string(rule.Status)
			}
			record := customeridentity.SubscriptionRecord{
				CustomerID: customerID, ID: rule.ID + ":" + pool.ID, RuleID: rule.ID,
				Name: pool.Name, Protocol: pool.Protocol,
				Endpoint: net.JoinHostPort(pool.Hostname, strconv.Itoa(pool.Port)), Status: status,
				Ready: false,
			}
			if binding, exists := activeBindings[rule.ID+"\x00"+pool.ID]; exists {
				record.Status = "unavailable"
				if rule.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && rule.Protocol == forwarding.ProtocolTCP &&
					!rule.Paused && rule.Deployed && rule.Status == forwarding.StatusActive && rule.IngressReadiness() == forwarding.IngressReady &&
					pool.Mode == endpoints.ModeSingleServiceEndpoint && pool.RuleID == rule.ID && pool.GroupID == rule.EntryGroupID {
					healthy := false
					for _, member := range r.store.endpointMembers[pool.ID] {
						if member.CandidateEligibleForNewConnection(time.Now().UTC(), endpoints.DefaultHealthTTL) {
							healthy = true
							break
						}
					}
					if healthy {
						uri, err := provisioningvless.URI(vlessProfile(pool, rule, binding.CredentialUUID))
						if err == nil {
							record.Status = string(forwarding.StatusActive)
							record.Ready = true
							record.URI = uri
						}
					}
				}
			}
			items = append(items, record)
		}
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Name == items[right].Name {
			return items[left].ID < items[right].ID
		}
		return items[left].Name < items[right].Name
	})
	return items, nil
}

func (r *customerIdentityRepository) Portal(_ context.Context) (customeridentity.PortalView, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	version := r.panelVersion
	if version == "" {
		version = "development"
	}
	view := customeridentity.PortalView{
		SiteName: r.store.siteSettings.SiteName, PanelVersion: version,
		SiteInfo:    []customeridentity.InfoItem{{Label: "面板标题", Value: r.store.siteSettings.PanelTitle}},
		BackendInfo: []customeridentity.InfoItem{{Label: "服务状态", Value: "运行中"}},
	}
	if description := strings.TrimSpace(r.store.siteSettings.PublicDescription); description != "" {
		view.SiteInfo = append(view.SiteInfo, customeridentity.InfoItem{Label: "站点说明", Value: description})
	}
	if supportURL := strings.TrimSpace(r.store.siteSettings.SupportURL); supportURL != "" {
		view.SiteInfo = append(view.SiteInfo, customeridentity.InfoItem{Label: "支持地址", Value: supportURL})
	}
	var selected *announcementCandidate
	for _, item := range r.store.announcements {
		if !item.Active(time.Now().UTC()) {
			continue
		}
		candidate := announcementCandidate{title: item.Title, content: item.Content, sortOrder: item.SortOrder, createdAt: item.CreatedAt, updatedAt: item.UpdatedAt}
		if selected == nil || candidate.less(*selected) {
			copy := candidate
			selected = &copy
		}
	}
	if selected != nil {
		updatedAt := selected.updatedAt
		view.Announcement = customeridentity.AnnouncementView{Title: selected.title, Content: selected.content, UpdatedAt: &updatedAt}
	}
	return view, nil
}

type announcementCandidate struct {
	title     string
	content   string
	sortOrder int
	createdAt time.Time
	updatedAt time.Time
}

func (candidate announcementCandidate) less(other announcementCandidate) bool {
	if candidate.sortOrder != other.sortOrder {
		return candidate.sortOrder < other.sortOrder
	}
	return candidate.createdAt.Before(other.createdAt)
}

func (r *customerIdentityRepository) AppendAudit(ctx context.Context, event audit.Event) error {
	return r.store.AppendAudit(ctx, event)
}
