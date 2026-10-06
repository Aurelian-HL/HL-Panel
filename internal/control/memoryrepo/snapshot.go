package memoryrepo

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

const SnapshotVersion = 16
const businessSnapshotVersion = 2
const networkPolicySnapshotVersion = 3
const ruleGroupsSnapshotVersion = 4
const siteSnapshotVersion = 5
const customerSessionsSnapshotVersion = 6
const vlessIdentitySnapshotVersion = 7
const vlessRuntimeSnapshotVersion = 8
const applyAttemptsSnapshotVersion = 9
const ruleGroupOwnershipSnapshotVersion = 10
const autoRealitySnapshotVersion = 11
const protocolHealthSnapshotVersion = 12
const protocolProbeSnapshotVersion = 13
const nezhaBindingSnapshotVersion = 14
const vlessSOCKS5SnapshotVersion = 15
const MaxSnapshotBytes = 16 << 20

func SupportsSnapshotVersion(version int) bool {
	return version >= 1 && version <= SnapshotVersion
}

// validPersistedTokenHash accepts only the canonical representation produced
// by securetoken.Hash.  A map key that merely has the right length is not a
// valid bearer-token binding: it would never match a freshly hashed token and
// could leave corrupted credentials in the restored authentication index.
func validPersistedTokenHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validPersistedIdentity(value string) bool {
	return strings.TrimSpace(value) != ""
}

// These private DTOs deliberately preserve hashes hidden by public JSON models.
// Snapshot bytes are confidential database records, never an API response.
type storedAdministrator struct {
	ID                 string
	Username           string
	PasswordHash       []byte
	CreatedAt          time.Time
	MustChangePassword bool
}

type storedCustomer struct {
	Customer     customers.Customer
	PasswordHash []byte
}

type storedNode struct {
	Node           json.RawMessage
	CredentialHash string
}

type storedRevision struct {
	Revision       generations.GroupRevision
	RequestSHA256  string
	IdempotencyKey string
}

type storedVLESSBinding struct {
	Binding        vlessidentity.Binding
	CredentialUUID string
}

// storedVLESSRuntimeMaterial is a confidential persistence DTO. Material's
// JSON tags intentionally omit its secrets, so they are repeated explicitly
// here and can only appear in the encrypted/permissioned snapshot boundary.
type storedVLESSRuntimeMaterial struct {
	Material          vlessruntime.Material
	CredentialUUID    string
	RealityPrivateKey string
}

// storedVLESSSOCKS5Upstream is a confidential snapshot-only DTO. It is never
// embedded in forwarding.Rule or returned from a public/list/transfer API.
type storedVLESSSOCKS5Upstream struct {
	Hostname string
	Port     int
	Username string
	Password string
}

type snapshot struct {
	Version               int
	Administrators        map[string]storedAdministrator
	Sessions              map[string]auth.Session
	CustomerSessions      map[string]customeridentity.Session
	Tokens                map[string]enrollment.Token
	Nodes                 map[string]storedNode
	NodesByCredential     map[string]string
	DeviceGroups          map[string]groups.DeviceGroup
	MembersByGroup        map[string]map[string]groups.Member
	EndpointPools         map[string]endpoints.EndpointPool
	EndpointMembers       map[string]map[string]endpoints.EndpointPoolMember
	EndpointCreateKeys    map[string]string
	EndpointMemberKeys    map[string]string
	EndpointCreateHashes  map[string]string
	EndpointMemberHashes  map[string]string
	RevisionsByGroup      map[string][]storedRevision
	RevisionByIdempotency map[string]map[string]string
	NodeConfigsByNode     map[string]map[int64]generations.NodeConfigGeneration
	NodeConfigsByRevision map[string][]generations.NodeConfigGeneration
	ApplyResultsByNode    map[string]map[int64]map[string]generations.ApplyResult
	ApplyAttemptsByNode   map[string]map[int64]generations.ApplyAttemptState
	AuditEvents           []audit.Event
	Customers             map[string]storedCustomer
	UserGroups            map[string]customers.UserGroup
	GroupNetworks         map[string]groupconfig.GroupNetwork
	ForwardRules          map[string]forwarding.Rule
	AutoRealityKeys       map[string]string
	VLESSSOCKS5Upstreams  map[string]storedVLESSSOCKS5Upstream
	RuleGroups            map[string]rulegroups.RuleGroup
	SiteSettings          siteconfig.Settings
	Announcements         map[string]announcements.Announcement
	VLESSBindings         map[string]storedVLESSBinding
	VLESSRuntimeMaterials map[string]storedVLESSRuntimeMaterial
	ProtocolHealth        map[string]map[string]gatewaymembership.ProtocolObservation
	ProtocolProbes        map[string]gatewaymembership.ProtocolProbeConfig
	BusinessIdempotency   map[string]businessMutation
}

// EncodeSnapshot is only for the confidential persistence adapter.
func (s *Store) EncodeSnapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// ForwardRules is a confidential persistence projection, but it must still
	// preserve the same credential boundary as the public rule API. Runtime
	// rules keep the SOCKS5 username so the Xray bundle compiler can use it;
	// snapshot credentials live in VLESSSOCKS5Upstreams and are the sole source
	// of username/password material during restore. Clone before redacting so
	// encoding never mutates the live rule map.
	forwardRules := make(map[string]forwarding.Rule, len(s.forwardRules))
	for id, rule := range s.forwardRules {
		projected := cloneForwardingRule(rule)
		if projected.EffectiveIngressProtocol() == forwarding.IngressVLESSReality && projected.VLESSOutboundMode == forwarding.VLESSOutboundSOCKS5 {
			projected.VLESSSOCKS5Username = ""
		}
		forwardRules[id] = projected
	}
	groupNetworks := make(map[string]groupconfig.GroupNetwork, len(s.groupNetworks))
	for id, network := range s.groupNetworks {
		normalized, err := groupconfig.NormalizeStoredNetwork(network)
		if err != nil {
			return nil, errors.New("encode invalid group network")
		}
		groupNetworks[id] = normalized
	}
	state := snapshot{
		Version:        SnapshotVersion,
		Administrators: make(map[string]storedAdministrator, len(s.adminsByUsername)),
		Sessions:       s.sessionsByHash, CustomerSessions: s.customerSessions, Tokens: s.tokensByHash,
		Nodes: make(map[string]storedNode, len(s.nodes)), NodesByCredential: s.nodesByCredential,
		DeviceGroups: s.deviceGroups, MembersByGroup: s.membersByGroup,
		EndpointPools: s.endpointPools, EndpointMembers: s.endpointMembers,
		EndpointCreateKeys: s.endpointCreateKeys, EndpointMemberKeys: s.endpointMemberKeys,
		EndpointCreateHashes: s.endpointCreateHashes, EndpointMemberHashes: s.endpointMemberHashes,
		RevisionsByGroup:      make(map[string][]storedRevision, len(s.revisionsByGroup)),
		RevisionByIdempotency: s.revisionByIdempotency,
		NodeConfigsByNode:     s.nodeConfigsByNode, NodeConfigsByRevision: s.nodeConfigsByRevision,
		ApplyResultsByNode: s.applyResultsByNode, ApplyAttemptsByNode: s.applyAttemptsByNode, AuditEvents: s.auditEvents,
		Customers:  make(map[string]storedCustomer, len(s.customers)),
		UserGroups: s.userGroups, GroupNetworks: groupNetworks,
		ForwardRules: forwardRules, AutoRealityKeys: s.autoRealityKeys, RuleGroups: s.ruleGroups,
		VLESSSOCKS5Upstreams: make(map[string]storedVLESSSOCKS5Upstream, len(s.vlessSOCKS5Upstreams)),
		SiteSettings:         s.siteSettings, Announcements: s.announcements,
		VLESSBindings:         make(map[string]storedVLESSBinding, len(s.vlessBindings)),
		VLESSRuntimeMaterials: make(map[string]storedVLESSRuntimeMaterial, len(s.vlessRuntimeMaterials)),
		BusinessIdempotency:   s.businessIdempotency,
		ProtocolHealth:        s.protocolHealth,
		ProtocolProbes:        s.protocolProbes,
	}
	for id, record := range s.vlessBindings {
		state.VLESSBindings[id] = storedVLESSBinding{Binding: cloneVLESSBinding(record.Binding), CredentialUUID: record.CredentialUUID}
	}
	for id, record := range s.vlessRuntimeMaterials {
		state.VLESSRuntimeMaterials[id] = storedVLESSRuntimeMaterial{
			Material: cloneVLESSRuntimeMaterial(record), CredentialUUID: record.CredentialUUID,
			RealityPrivateKey: record.RealityPrivateKey,
		}
	}
	for id, upstream := range s.vlessSOCKS5Upstreams {
		normalized, err := normalizeVLESSSOCKS5Upstream(id, upstream)
		if err != nil {
			return nil, errors.New("encode invalid VLESS SOCKS5 upstream")
		}
		if _, exists := s.forwardRules[id]; !exists {
			return nil, errors.New("encode orphan VLESS SOCKS5 upstream")
		}
		state.VLESSSOCKS5Upstreams[id] = storedVLESSSOCKS5Upstream{
			Hostname: normalized.Hostname, Port: normalized.Port,
			Username: normalized.Username, Password: normalized.Password,
		}
	}
	for id, customer := range s.customers {
		state.Customers[id] = storedCustomer{Customer: customer, PasswordHash: customer.PasswordHash}
	}
	for key, admin := range s.adminsByUsername {
		state.Administrators[key] = storedAdministrator(admin)
	}
	for key, node := range s.nodes {
		raw, err := json.Marshal(node)
		if err != nil {
			return nil, errors.New("encode node persistence state")
		}
		state.Nodes[key] = storedNode{Node: raw, CredentialHash: node.CredentialHash}
	}
	for key, revisions := range s.revisionsByGroup {
		items := make([]storedRevision, len(revisions))
		for i, revision := range revisions {
			items[i] = storedRevision{Revision: revision, RequestSHA256: revision.RequestSHA256, IdempotencyKey: revision.IdempotencyKey}
		}
		state.RevisionsByGroup[key] = items
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, errors.New("encode persistence state")
	}
	if len(raw) > MaxSnapshotBytes {
		return nil, errors.New("persistence state exceeds snapshot size limit")
	}
	return raw, nil
}

// DecodeSnapshot fails closed. A damaged or newer snapshot is never an empty DB.
func DecodeSnapshot(raw []byte) (*Store, error) {
	if len(raw) == 0 || len(raw) > MaxSnapshotBytes {
		return nil, errors.New("invalid persistence state size")
	}
	var state snapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, errors.New("invalid persistence state encoding")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing persistence state data")
	}
	if !SupportsSnapshotVersion(state.Version) {
		return nil, errors.New("unsupported persistence state version")
	}
	if state.Version < ruleGroupOwnershipSnapshotVersion {
		for _, group := range state.RuleGroups {
			if group.OwnerAdministratorID != "" {
				return nil, errors.New("legacy snapshot contains unexpected rule-group ownership")
			}
		}
	}
	if len(state.Administrators) == 0 || state.Sessions == nil || state.Tokens == nil || state.Nodes == nil ||
		state.NodesByCredential == nil || state.DeviceGroups == nil || state.MembersByGroup == nil ||
		state.EndpointPools == nil || state.EndpointMembers == nil || state.EndpointCreateKeys == nil ||
		state.EndpointMemberKeys == nil || state.EndpointCreateHashes == nil || state.EndpointMemberHashes == nil ||
		state.RevisionsByGroup == nil || state.RevisionByIdempotency == nil || state.NodeConfigsByNode == nil ||
		state.NodeConfigsByRevision == nil || state.ApplyResultsByNode == nil || state.AuditEvents == nil {
		return nil, errors.New("incomplete persistence state")
	}
	s := New(auth.Administrator{})
	if state.Version == 1 {
		if state.Customers != nil || state.UserGroups != nil || state.GroupNetworks != nil || state.ForwardRules != nil || state.BusinessIdempotency != nil {
			return nil, errors.New("legacy snapshot contains unexpected business state")
		}
	} else {
		if state.Customers == nil || state.UserGroups == nil || state.GroupNetworks == nil || state.ForwardRules == nil || state.BusinessIdempotency == nil {
			return nil, errors.New("incomplete business persistence state")
		}
		if state.Version >= ruleGroupsSnapshotVersion && state.RuleGroups == nil {
			return nil, errors.New("incomplete rule-group persistence state")
		}
		if state.Version >= siteSnapshotVersion && (state.Announcements == nil || siteconfig.ValidateStored(state.SiteSettings) != nil) {
			return nil, errors.New("incomplete site persistence state")
		}
		if state.Version >= customerSessionsSnapshotVersion && state.CustomerSessions == nil {
			return nil, errors.New("incomplete customer session persistence state")
		}
		if state.Version >= vlessIdentitySnapshotVersion && state.VLESSBindings == nil {
			return nil, errors.New("incomplete VLESS identity persistence state")
		}
		if state.Version >= vlessRuntimeSnapshotVersion && state.VLESSRuntimeMaterials == nil {
			return nil, errors.New("incomplete VLESS runtime material persistence state")
		}
		if state.Version >= autoRealitySnapshotVersion && state.AutoRealityKeys == nil {
			return nil, errors.New("incomplete automatic Reality persistence state")
		}
		if state.Version >= protocolHealthSnapshotVersion && state.ProtocolHealth == nil {
			return nil, errors.New("incomplete protocol health persistence state")
		}
		if state.Version >= protocolProbeSnapshotVersion && state.ProtocolProbes == nil {
			return nil, errors.New("incomplete protocol probe persistence state")
		}
		if state.Version >= applyAttemptsSnapshotVersion && state.ApplyAttemptsByNode == nil {
			return nil, errors.New("incomplete apply attempt persistence state")
		}
		// A snapshot produced by a newer binary and deliberately downgraded for
		// migration tests can still contain the field as an empty map.  That is
		// equivalent to the field being absent; only non-empty runtime material
		// would represent data that an older schema cannot safely understand.
		if state.Version < vlessRuntimeSnapshotVersion && len(state.VLESSRuntimeMaterials) > 0 {
			return nil, errors.New("legacy snapshot contains unexpected VLESS runtime material state")
		}
		for id, stored := range state.Customers {
			if id != stored.Customer.ID || auth.ValidatePasswordHash(stored.PasswordHash) != nil {
				return nil, errors.New("invalid persisted customer identity")
			}
			customer := stored.Customer
			customer.PasswordHash = append([]byte(nil), stored.PasswordHash...)
			s.customers[id] = customer
		}
		for id, network := range state.GroupNetworks {
			normalized, err := groupconfig.NormalizeStoredNetwork(network)
			if err != nil {
				return nil, errors.New("invalid group network policy")
			}
			state.GroupNetworks[id] = normalized
		}
		s.userGroups, s.groupNetworks, s.forwardRules, s.businessIdempotency = state.UserGroups, state.GroupNetworks, state.ForwardRules, state.BusinessIdempotency
		if state.AutoRealityKeys != nil {
			s.autoRealityKeys = make(map[string]string, len(state.AutoRealityKeys))
			for ruleID, privateKey := range state.AutoRealityKeys {
				// A prior delete path failed to remove the confidential runtime
				// key. It has no usable owner once its rule is gone, so discard
				// only that orphan during restore; existing-rule mismatches still
				// fail closed in validateBusinessSnapshot below.
				if rule, exists := state.ForwardRules[ruleID]; !exists || rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality {
					continue
				}
				s.autoRealityKeys[ruleID] = privateKey
			}
		}
		if state.Version >= vlessSOCKS5SnapshotVersion && state.VLESSSOCKS5Upstreams == nil {
			return nil, errors.New("incomplete VLESS SOCKS5 upstream persistence state")
		}
		if state.Version < vlessSOCKS5SnapshotVersion && len(state.VLESSSOCKS5Upstreams) > 0 {
			return nil, errors.New("legacy snapshot contains unexpected VLESS SOCKS5 upstream state")
		}
		for id, stored := range state.VLESSSOCKS5Upstreams {
			if _, exists := state.ForwardRules[id]; !exists {
				return nil, errors.New("invalid persisted VLESS SOCKS5 upstream rule")
			}
			upstream, err := normalizeVLESSSOCKS5Upstream(id, provisioningvless.SOCKS5Upstream{
				Hostname: stored.Hostname, Port: stored.Port, Username: stored.Username, Password: stored.Password,
			})
			if err != nil {
				return nil, errors.New("invalid persisted VLESS SOCKS5 upstream")
			}
			if s.vlessSOCKS5Upstreams == nil {
				s.vlessSOCKS5Upstreams = make(map[string]provisioningvless.SOCKS5Upstream)
			}
			s.vlessSOCKS5Upstreams[id] = upstream
		}
		if state.ProtocolHealth != nil {
			s.protocolHealth = state.ProtocolHealth
		}
		if state.ProtocolProbes != nil {
			for ruleID, probe := range state.ProtocolProbes {
				if ruleID == "" || !vlessruntime.ValidCredentialUUID(probe.UUID) || probe.EchoPort < 1 || probe.EchoPort > 65535 {
					return nil, errors.New("invalid persisted protocol probe")
				}
				s.protocolProbes[ruleID] = probe
			}
		}
		if state.RuleGroups != nil {
			s.ruleGroups = state.RuleGroups
		}
		if state.Version >= siteSnapshotVersion {
			s.siteSettings, s.announcements = state.SiteSettings, state.Announcements
		}
		if state.CustomerSessions != nil {
			s.customerSessions = state.CustomerSessions
		}
		if state.VLESSBindings != nil {
			for id, stored := range state.VLESSBindings {
				s.vlessBindings[id] = vlessidentity.CredentialRecord{Binding: cloneVLESSBinding(stored.Binding), CredentialUUID: stored.CredentialUUID}
			}
		}
		if state.VLESSRuntimeMaterials != nil {
			for id, stored := range state.VLESSRuntimeMaterials {
				material := cloneVLESSRuntimeMaterial(stored.Material)
				material.CredentialUUID = stored.CredentialUUID
				material.RealityPrivateKey = stored.RealityPrivateKey
				s.vlessRuntimeMaterials[id] = material
			}
		}
	}
	s.adminsByUsername = make(map[string]auth.Administrator, len(state.Administrators))
	adminIDs := make(map[string]struct{}, len(state.Administrators))
	for key, admin := range state.Administrators {
		if !validPersistedIdentity(key) || key != admin.Username || !validPersistedIdentity(admin.ID) || auth.ValidatePasswordHash(admin.PasswordHash) != nil {
			return nil, errors.New("invalid persisted administrator")
		}
		if _, duplicate := adminIDs[admin.ID]; duplicate {
			return nil, errors.New("duplicate persisted administrator identity")
		}
		adminIDs[admin.ID] = struct{}{}
		s.adminsByUsername[key] = auth.Administrator(admin)
	}
	boundNezhaIDs := make(map[uint64]string)
	for key, stored := range state.Nodes {
		var node = s.nodes[key]
		if json.Unmarshal(stored.Node, &node) != nil || node.ID != key || stored.CredentialHash == "" || state.NodesByCredential[stored.CredentialHash] != key {
			return nil, errors.New("invalid persisted node identity")
		}
		node.CredentialHash = stored.CredentialHash
		if node.NezhaServerID != 0 {
			if state.Version < nezhaBindingSnapshotVersion {
				return nil, errors.New("legacy snapshot contains Nezha binding")
			}
			if _, duplicate := boundNezhaIDs[node.NezhaServerID]; duplicate {
				return nil, errors.New("duplicate persisted Nezha binding")
			}
			boundNezhaIDs[node.NezhaServerID] = key
		}
		s.nodes[key] = node
	}
	for key, revisions := range state.RevisionsByGroup {
		items := make([]generations.GroupRevision, len(revisions))
		for i, stored := range revisions {
			items[i] = stored.Revision
			items[i].RequestSHA256 = stored.RequestSHA256
			items[i].IdempotencyKey = stored.IdempotencyKey
		}
		s.revisionsByGroup[key] = items
	}
	// Administrator sessions are bearer credentials. Validate the complete
	// binding before exposing the restored store: accepting an unknown admin ID,
	// malformed token hash, duplicate session ID, or already-invalid time range
	// would let a corrupted snapshot bypass the administrator boundary.
	adminSessionIDs := make(map[string]struct{}, len(state.Sessions))
	s.sessionsByHash = make(map[string]auth.Session, len(state.Sessions))
	for hash, session := range state.Sessions {
		if !validPersistedTokenHash(hash) || session.TokenHash != hash || !validPersistedIdentity(session.ID) || !validPersistedIdentity(session.AdminID) ||
			session.CreatedAt.IsZero() || !session.ExpiresAt.After(session.CreatedAt) {
			return nil, errors.New("invalid persisted administrator session")
		}
		if _, exists := adminIDs[session.AdminID]; !exists {
			return nil, errors.New("administrator session references missing administrator")
		}
		if _, duplicate := adminSessionIDs[session.ID]; duplicate {
			return nil, errors.New("duplicate persisted administrator session identity")
		}
		adminSessionIDs[session.ID] = struct{}{}
		s.sessionsByHash[hash] = session
	}
	s.tokensByHash = state.Tokens
	pendingNezhaIDs := make(map[uint64]struct{})
	for _, token := range s.tokensByHash {
		if token.NezhaServerID == 0 {
			continue
		}
		if state.Version < nezhaBindingSnapshotVersion || token.GroupID == "" {
			return nil, errors.New("invalid persisted Nezha enrollment")
		}
		if token.UsedAt != nil {
			if boundNezhaIDs[token.NezhaServerID] == "" {
				return nil, errors.New("consumed Nezha enrollment has no bound node")
			}
		} else if token.RevokedAt == nil && token.ExpiresAt.After(time.Now().UTC()) && boundNezhaIDs[token.NezhaServerID] != "" {
			return nil, errors.New("pending Nezha enrollment conflicts with bound node")
		} else if token.RevokedAt == nil && token.ExpiresAt.After(time.Now().UTC()) {
			if _, duplicate := pendingNezhaIDs[token.NezhaServerID]; duplicate {
				return nil, errors.New("duplicate pending Nezha enrollment")
			}
			pendingNezhaIDs[token.NezhaServerID] = struct{}{}
		}
	}
	customerSessionIDs := make(map[string]struct{}, len(s.customerSessions))
	for hash, session := range s.customerSessions {
		if !validPersistedTokenHash(hash) || session.TokenHash != hash || !validPersistedIdentity(session.ID) || !validPersistedIdentity(session.CustomerID) || session.CreatedAt.IsZero() || !session.ExpiresAt.After(session.CreatedAt) {
			return nil, errors.New("invalid persisted customer session")
		}
		if _, exists := s.customers[session.CustomerID]; !exists {
			return nil, errors.New("persisted customer session references missing customer")
		}
		if _, duplicate := customerSessionIDs[session.ID]; duplicate {
			return nil, errors.New("duplicate persisted customer session identity")
		}
		customerSessionIDs[session.ID] = struct{}{}
	}
	s.nodesByCredential = state.NodesByCredential
	s.deviceGroups, s.membersByGroup = state.DeviceGroups, state.MembersByGroup
	// MetadataRevision was introduced after the original snapshot format. A
	// restored legacy group is editable at revision one, while data-plane
	// CurrentRevision remains untouched.
	for id, group := range s.deviceGroups {
		if group.MetadataRevision == 0 {
			group.MetadataRevision = 1
			s.deviceGroups[id] = group
		}
	}
	s.endpointPools, s.endpointMembers = state.EndpointPools, state.EndpointMembers
	s.endpointCreateKeys, s.endpointMemberKeys = state.EndpointCreateKeys, state.EndpointMemberKeys
	s.endpointCreateHashes, s.endpointMemberHashes = state.EndpointCreateHashes, state.EndpointMemberHashes
	s.revisionByIdempotency = state.RevisionByIdempotency
	s.nodeConfigsByNode, s.nodeConfigsByRevision = state.NodeConfigsByNode, state.NodeConfigsByRevision
	s.applyResultsByNode, s.auditEvents = state.ApplyResultsByNode, state.AuditEvents
	if state.ApplyAttemptsByNode != nil {
		s.applyAttemptsByNode = state.ApplyAttemptsByNode
	}
	for nodeID, generationsByNode := range s.applyAttemptsByNode {
		if _, exists := s.nodes[nodeID]; !exists || generationsByNode == nil {
			return nil, errors.New("invalid persisted apply attempt node")
		}
		for generation, attempt := range generationsByNode {
			if generation <= 0 || s.nodeConfigsByNode[nodeID][generation].Generation != generation ||
				!validPersistedAttemptID(attempt.CurrentID) || !attempt.SeenIDs[attempt.CurrentID] {
				return nil, errors.New("invalid persisted apply attempt")
			}
			for seenID, seen := range attempt.SeenIDs {
				if !seen || !validPersistedAttemptID(seenID) {
					return nil, errors.New("invalid persisted apply attempt history")
				}
			}
			for _, result := range s.applyResultsByNode[nodeID][generation] {
				if result.AttemptID != attempt.CurrentID {
					return nil, errors.New("persisted apply result does not match current attempt")
				}
			}
		}
	}
	if err := s.validateBusinessSnapshot(); err != nil {
		return nil, err
	}
	// Reject missing inner maps before any domain operation can mutate state.
	for id := range s.deviceGroups {
		if s.membersByGroup[id] == nil || s.revisionByIdempotency[id] == nil {
			return nil, errors.New("invalid persisted group indexes")
		}
	}
	for id := range s.endpointPools {
		if s.endpointMembers[id] == nil {
			return nil, errors.New("invalid persisted endpoint indexes")
		}
	}
	return s, nil
}

func validPersistedAttemptID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
