package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/hostgeo"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
	"github.com/hongle/hl-panel/internal/control/nezhamonitor"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/panelmigration"
	"github.com/hongle/hl-panel/internal/control/panelruntime"
	"github.com/hongle/hl-panel/internal/control/panelupdate"
	"github.com/hongle/hl-panel/internal/control/releases"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/targetprobe"
	usagehttpapi "github.com/hongle/hl-panel/internal/control/usage/httpapi"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/control/vlesssetup"
)

type PlatformInfo struct {
	Version   string
	BuildTime string
}

type API struct {
	auth               *auth.Service
	enrollment         *enrollment.Service
	nodes              *nodes.Service
	groups             *groups.Service
	endpoints          *endpoints.Service
	generations        *generations.Service
	logger             *slog.Logger
	customers          *customers.Service
	forwarding         *forwarding.Service
	groupNetworks      *groupconfig.Service
	ruleGroups         *rulegroups.Service
	siteConfig         *siteconfig.Service
	subscriptions      *subscriptions.Service
	announcements      *announcements.Service
	vlessIdentity      *vlessidentity.Service
	vlessRuntime       *vlessruntime.Service
	vlessSetup         *vlesssetup.Service
	platformInfo       PlatformInfo
	panelRuntime       *panelruntime.Service
	usage              usagehttpapi.Service
	gatewayMembership  *gatewaymembership.Handler
	nezha              *nezhamonitor.Service
	hostGeo            *hostgeo.Service
	targetProbe        *targetprobe.Service
	releases           *releases.Service
	migration          *migrationbackup.Service
	panelUpdate        *panelupdate.Service
	automaticMigration *panelmigration.Service
}

func WithMigrationBackup(service *migrationbackup.Service) Option {
	return func(api *API) { api.migration = service }
}

func WithAutomaticMigration(service *panelmigration.Service) Option {
	return func(api *API) { api.automaticMigration = service }
}

func WithTargetProbe(service *targetprobe.Service) Option {
	return func(api *API) { api.targetProbe = service }
}

func WithNezha(service *nezhamonitor.Service) Option {
	return func(api *API) { api.nezha = service }
}

func WithHostGeo(service *hostgeo.Service) Option {
	return func(api *API) { api.hostGeo = service }
}

func WithRuleGroups(service *rulegroups.Service) Option {
	return func(api *API) { api.ruleGroups = service }
}

func WithSite(config *siteconfig.Service, notices *announcements.Service, platform PlatformInfo) Option {
	return func(api *API) {
		api.siteConfig, api.announcements, api.platformInfo = config, notices, platform
	}
}

func WithUsage(service usagehttpapi.Service) Option {
	return func(api *API) { api.usage = service }
}

func WithPanelRuntime(service *panelruntime.Service) Option {
	return func(api *API) { api.panelRuntime = service }
}

func WithGatewayMembership(handler *gatewaymembership.Handler) Option {
	return func(api *API) { api.gatewayMembership = handler }
}

func WithVLESS(identityService *vlessidentity.Service, runtimeService *vlessruntime.Service) Option {
	return func(api *API) {
		api.vlessIdentity, api.vlessRuntime = identityService, runtimeService
	}
}

func WithSubscriptions(service *subscriptions.Service) Option {
	return func(api *API) { api.subscriptions = service }
}

type Option func(*API)

func WithBusiness(customerService *customers.Service, forwardingService *forwarding.Service, networkService *groupconfig.Service) Option {
	return func(api *API) {
		api.customers, api.forwarding, api.groupNetworks = customerService, forwardingService, networkService
	}
}

func New(authService *auth.Service, enrollmentService *enrollment.Service, nodeService *nodes.Service, groupService *groups.Service, endpointService *endpoints.Service, generationService *generations.Service, logger *slog.Logger, options ...Option) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	api := &API{
		auth:        authService,
		enrollment:  enrollmentService,
		nodes:       nodeService,
		groups:      groupService,
		endpoints:   endpointService,
		generations: generationService,
		logger:      logger,
	}
	for _, option := range options {
		option(api)
	}
	if api.groupNetworks != nil && api.endpoints != nil && api.vlessIdentity != nil {
		api.vlessSetup = vlesssetup.NewService(api.groupNetworks, api.endpoints, api.vlessIdentity)
	}
	mux := http.NewServeMux()
	api.registerSubscriptionRoutes(mux)
	api.registerBusinessRoutes(mux)
	api.registerSiteRoutes(mux)
	if api.gatewayMembership != nil {
		api.gatewayMembership.Register(mux)
	}
	if api.usage != nil {
		usageHandler, err := usagehttpapi.New(api.usage, api.authenticateUsageNode, api.authorizeUsageAdministrator)
		if err != nil {
			panic("configure usage HTTP handler: " + err.Error())
		}
		if err := usageHandler.Register(mux); err != nil {
			panic("register usage HTTP handler: " + err.Error())
		}
	}
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("POST /api/v1/auth/login", api.login)
	mux.HandleFunc("GET /api/v1/auth/me", api.requireAdministratorSession(api.currentAdministrator))
	if api.releases != nil {
		mux.HandleFunc("GET /api/v1/system/version", api.requireAdministratorSession(api.checkVersion))
	}
	if api.panelUpdate != nil {
		mux.HandleFunc("GET /api/v1/panel/update", api.requireAdministrator(api.panelUpdateStatus))
		mux.HandleFunc("POST /api/v1/panel/update", api.requireAdministrator(api.startPanelUpdate))
	}
	mux.HandleFunc("PUT /api/v1/auth/password", api.requireAdministratorSession(api.changeAdministratorPassword))
	mux.HandleFunc("GET /api/v1/overview", api.requireAdministrator(api.overview))
	if api.migration != nil {
		mux.HandleFunc("POST /api/v1/panel/migration/export", api.requireAdministrator(api.exportMigration))
		mux.HandleFunc("POST /api/v1/panel/migration/preview", api.requireAdministrator(api.inspectMigration))
		mux.HandleFunc("POST /api/v1/panel/migration/import", api.requireAdministrator(api.importMigration))
		mux.HandleFunc("GET /api/v1/panel/migration/recovery/{backup_id}", api.requireAdministrator(api.migrationRecovery))
	}
	if api.automaticMigration != nil {
		mux.HandleFunc("GET /api/v1/panel/migration/auto", api.requireAdministrator(api.automaticMigrationStatus))
		mux.HandleFunc("POST /api/v1/panel/migration/auto", api.requireAdministrator(api.startAutomaticMigration))
		mux.HandleFunc("POST /api/v1/panel/migration/auto/probe", api.requireAdministrator(api.probeAutomaticMigration))
		mux.HandleFunc("POST /api/v1/panel/migration/auto/continue", api.requireAdministrator(api.continueAutomaticMigration))
		mux.HandleFunc("POST /api/v1/panel/migration/auto/rollback", api.requireAdministrator(api.rollbackAutomaticMigration))
	}
	if api.panelRuntime != nil {
		mux.HandleFunc("GET /api/v1/panel/logs", api.requireAdministrator(api.panelLogs))
		mux.HandleFunc("GET /api/v1/panel/runtime", api.requireAdministrator(api.panelRuntimeStatus))
		mux.HandleFunc("POST /api/v1/panel/control", api.requireAdministrator(api.requestPanelControl))
		mux.HandleFunc("GET /api/v1/panel/control", api.requireAdministrator(api.panelControl))
	}
	mux.HandleFunc("DELETE /api/v1/nodes/{node_id}", api.requireAdministrator(api.deleteNode))
	mux.HandleFunc("GET /api/v1/nodes", api.requireAdministrator(api.listNodes))
	mux.HandleFunc("POST /api/v1/nodes/{node_id}/control", api.requireAdministrator(api.requestNodeControl))
	mux.HandleFunc("GET /api/v1/nodes/{node_id}/control", api.requireAdministrator(api.nodeControl))
	mux.HandleFunc("POST /api/v1/nodes/{node_id}/credential/rotate", api.requireAdministrator(api.rotateNodeCredential))
	mux.HandleFunc("POST /api/v1/enrollment-tokens", api.requireAdministrator(api.issueEnrollmentToken))
	mux.HandleFunc("POST /api/v1/enrollment-tokens/{token_id}/revoke", api.requireAdministrator(api.revokeEnrollmentToken))
	mux.HandleFunc("GET /api/v1/device-groups", api.requireAdministrator(api.listDeviceGroups))
	mux.HandleFunc("GET /api/v1/device-groups/{group_id}/enrollment-tokens", api.requireAdministrator(api.listPendingGroupEnrollmentTokens))
	mux.HandleFunc("POST /api/v1/device-groups", api.requireAdministrator(api.createDeviceGroup))
	mux.HandleFunc("PUT /api/v1/device-groups/{group_id}", api.requireAdministrator(api.updateDeviceGroup))
	mux.HandleFunc("DELETE /api/v1/device-groups/{group_id}", api.requireAdministrator(api.deleteDeviceGroup))
	mux.HandleFunc("GET /api/v1/device-groups/{group_id}/members", api.requireAdministrator(api.listDeviceGroupMembers))
	mux.HandleFunc("GET /api/v1/device-groups/{group_id}/monitoring/nezha", api.requireAdministrator(api.listNezhaGroupMonitoring))
	mux.HandleFunc("GET /api/v1/monitoring/nezha/servers", api.requireAdministrator(api.listNezhaInventory))
	mux.HandleFunc("POST /api/v1/device-groups/{group_id}/members", api.requireAdministrator(api.addDeviceGroupMember))
	mux.HandleFunc("PUT /api/v1/device-groups/{group_id}/members/{node_id}/weight", api.requireAdministrator(api.updateDeviceGroupMemberWeight))
	mux.HandleFunc("POST /api/v1/device-groups/{group_id}/members/{node_id}/retire", api.requireAdministrator(api.retireDeviceGroupMember))
	mux.HandleFunc("POST /api/v1/device-groups/{group_id}/generations", api.requireAdministrator(api.createGroupRevision))
	mux.HandleFunc("POST /api/v1/endpoint-pools", api.requireAdministrator(api.createEndpointPool))
	mux.HandleFunc("GET /api/v1/endpoint-pools", api.requireAdministrator(api.listEndpointPools))
	mux.HandleFunc("DELETE /api/v1/endpoint-pools/{pool_id}", api.requireAdministrator(api.deleteEndpointPool))
	mux.HandleFunc("POST /api/v1/endpoint-pools/{pool_id}/members", api.requireAdministrator(api.addEndpointPoolMember))
	mux.HandleFunc("POST /api/v1/agent/enroll", api.enrollNode)
	mux.HandleFunc("POST /api/v1/agent/reenroll", api.reenrollNode)
	mux.HandleFunc("POST /api/v1/agent/heartbeat", api.requireNode(api.heartbeat))
	mux.HandleFunc("GET /api/v1/agent/control", api.requireNode(api.desiredControl))
	mux.HandleFunc("POST /api/v1/agent/control-results", api.requireNode(api.recordControlResult))
	mux.HandleFunc("GET /api/v1/agent/desired", api.requireNode(api.desiredNodeConfig))
	mux.HandleFunc("GET /api/v1/agent/desired-watch", api.requireNode(api.watchDesiredNodeConfig))
	mux.HandleFunc("POST /api/v1/agent/apply-results", api.requireNode(api.recordApplyResult))
	if api.vlessIdentity != nil {
		mux.HandleFunc("GET /api/v1/vless/identities", api.requireAdministrator(api.listVLESSIdentities))
		mux.HandleFunc("GET /api/v1/vless/identities/{id}/connection", api.requireAdministrator(api.getVLESSIdentityConnection))
		mux.HandleFunc("POST /api/v1/vless/identities", api.requireAdministrator(api.provisionVLESSIdentity))
		mux.HandleFunc("POST /api/v1/vless/identities/{id}/rotate", api.requireAdministrator(api.rotateVLESSIdentity))
		mux.HandleFunc("POST /api/v1/vless/identities/{id}/revoke", api.requireAdministrator(api.revokeVLESSIdentity))
	}
	if api.vlessRuntime != nil {
		mux.HandleFunc("GET /api/v1/vless/runtime-materials", api.requireAdministrator(api.listVLESSRuntimeMaterials))
		mux.HandleFunc("POST /api/v1/vless/runtime-materials", api.requireAdministrator(api.bindVLESSRuntimeMaterial))
		mux.HandleFunc("POST /api/v1/vless/runtime-materials/{id}/revoke", api.requireAdministrator(api.revokeVLESSRuntimeMaterial))
	}
	return api.securityHeaders(api.recoverPanics(mux))
}

func (api *API) authenticateUsageNode(request *http.Request) (string, error) {
	node, err := api.nodes.AuthenticateCredential(request.Context(), bearerToken(request.Header.Get("Authorization")))
	if err != nil {
		return "", err
	}
	return node.ID, nil
}

func (api *API) authorizeUsageAdministrator(request *http.Request) (string, error) {
	session, err := api.auth.Authenticate(request.Context(), bearerToken(request.Header.Get("Authorization")))
	if err != nil {
		return "", err
	}
	if session.MustChangePassword {
		return "", faults.ErrPasswordChangeRequired
	}
	return session.AdminID, nil
}

type administratorHandler func(http.ResponseWriter, *http.Request, auth.Session)

func (api *API) requireAdministrator(next administratorHandler) http.HandlerFunc {
	return api.requireAdministratorSession(func(writer http.ResponseWriter, request *http.Request, session auth.Session) {
		if session.MustChangePassword {
			writeJSON(writer, http.StatusForbidden, problemEnvelope{Error: problem{Code: "password_change_required", Message: "请先在个人中心修改初始密码"}})
			return
		}
		next(writer, request, session)
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			api.logger.Info("管理员操作请求：" + request.Pattern)
		}
	})
}

func (api *API) requireAdministratorSession(next administratorHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		rawToken := bearerToken(request.Header.Get("Authorization"))
		session, err := api.auth.Authenticate(request.Context(), rawToken)
		if err != nil {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(writer, request, err)
			return
		}
		next(writer, request, session)
	}
}

type nodeHandler func(http.ResponseWriter, *http.Request, nodes.Node)

func (api *API) requireNode(next nodeHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		rawCredential := bearerToken(request.Header.Get("Authorization"))
		node, err := api.nodes.AuthenticateCredential(request.Context(), rawCredential)
		if err != nil {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(writer, request, err)
			return
		}
		next(writer, request, node)
	}
}

func bearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func (api *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

func (api *API) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				api.logger.Error("http handler panic", "method", request.Method, "path", safeRequestPath(request.URL.Path))
				writeInternalProblem(writer)
			}
		}()
		next.ServeHTTP(writer, request)
	})
}
