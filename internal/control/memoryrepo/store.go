package memoryrepo

import (
	"sync"

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
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

type Store struct {
	mu sync.RWMutex

	adminsByUsername map[string]auth.Administrator
	sessionsByHash   map[string]auth.Session
	customerSessions map[string]customeridentity.Session
	tokensByHash     map[string]enrollment.Token

	nodes                map[string]nodes.Node
	nodesByCredential    map[string]string
	deviceGroups         map[string]groups.DeviceGroup
	membersByGroup       map[string]map[string]groups.Member
	endpointPools        map[string]endpoints.EndpointPool
	endpointMembers      map[string]map[string]endpoints.EndpointPoolMember
	endpointCreateKeys   map[string]string
	endpointMemberKeys   map[string]string
	endpointCreateHashes map[string]string
	endpointMemberHashes map[string]string

	revisionsByGroup      map[string][]generations.GroupRevision
	revisionByIdempotency map[string]map[string]string
	nodeConfigsByNode     map[string]map[int64]generations.NodeConfigGeneration
	nodeConfigsByRevision map[string][]generations.NodeConfigGeneration
	applyResultsByNode    map[string]map[int64]map[string]generations.ApplyResult
	applyAttemptsByNode   map[string]map[int64]generations.ApplyAttemptState
	auditEvents           []audit.Event
	customers             map[string]customers.Customer
	userGroups            map[string]customers.UserGroup
	groupNetworks         map[string]groupconfig.GroupNetwork
	forwardRules          map[string]forwarding.Rule
	autoRealityKeys       map[string]string
	// vlessSOCKS5Upstreams is confidential runtime material indexed by forwarding
	// rule ID. It is deliberately separate from forwarding.Rule so public
	// projections, audit records and transfer payloads cannot carry passwords.
	vlessSOCKS5Upstreams  map[string]provisioningvless.SOCKS5Upstream
	ruleGroups            map[string]rulegroups.RuleGroup
	siteSettings          siteconfig.Settings
	subscriptions         map[string]subscriptions.Record
	announcements         map[string]announcements.Announcement
	vlessBindings         map[string]vlessidentity.CredentialRecord
	vlessRuntimeMaterials map[string]vlessruntime.Material
	protocolHealth        map[string]map[string]gatewaymembership.ProtocolObservation
	protocolProbes        map[string]gatewaymembership.ProtocolProbeConfig
	businessIdempotency   map[string]businessMutation
}

func New(admin auth.Administrator) *Store {
	return &Store{
		adminsByUsername:      map[string]auth.Administrator{admin.Username: cloneAdministrator(admin)},
		sessionsByHash:        make(map[string]auth.Session),
		customerSessions:      make(map[string]customeridentity.Session),
		tokensByHash:          make(map[string]enrollment.Token),
		nodes:                 make(map[string]nodes.Node),
		nodesByCredential:     make(map[string]string),
		deviceGroups:          make(map[string]groups.DeviceGroup),
		membersByGroup:        make(map[string]map[string]groups.Member),
		endpointPools:         make(map[string]endpoints.EndpointPool),
		endpointMembers:       make(map[string]map[string]endpoints.EndpointPoolMember),
		endpointCreateKeys:    make(map[string]string),
		endpointMemberKeys:    make(map[string]string),
		endpointCreateHashes:  make(map[string]string),
		endpointMemberHashes:  make(map[string]string),
		revisionsByGroup:      make(map[string][]generations.GroupRevision),
		revisionByIdempotency: make(map[string]map[string]string),
		nodeConfigsByNode:     make(map[string]map[int64]generations.NodeConfigGeneration),
		nodeConfigsByRevision: make(map[string][]generations.NodeConfigGeneration),
		applyResultsByNode:    make(map[string]map[int64]map[string]generations.ApplyResult),
		applyAttemptsByNode:   make(map[string]map[int64]generations.ApplyAttemptState),
		auditEvents:           make([]audit.Event, 0),
		customers:             make(map[string]customers.Customer),
		userGroups:            make(map[string]customers.UserGroup),
		groupNetworks:         make(map[string]groupconfig.GroupNetwork),
		forwardRules:          make(map[string]forwarding.Rule),
		autoRealityKeys:       make(map[string]string),
		vlessSOCKS5Upstreams:  make(map[string]provisioningvless.SOCKS5Upstream),
		ruleGroups:            make(map[string]rulegroups.RuleGroup),
		siteSettings:          siteconfig.DefaultSettings(),
		subscriptions:         make(map[string]subscriptions.Record),
		announcements:         make(map[string]announcements.Announcement),
		vlessBindings:         make(map[string]vlessidentity.CredentialRecord),
		vlessRuntimeMaterials: make(map[string]vlessruntime.Material),
		protocolHealth:        make(map[string]map[string]gatewaymembership.ProtocolObservation),
		protocolProbes:        make(map[string]gatewaymembership.ProtocolProbeConfig),
		businessIdempotency:   make(map[string]businessMutation),
	}
}
