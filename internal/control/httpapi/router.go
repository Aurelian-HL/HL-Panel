package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/diagnostics"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nezhamonitor"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
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
	auth              *auth.Service
	enrollment        *enrollment.Service
	nodes             *nodes.Service
	groups            *groups.Service
	endpoints         *endpoints.Service
	generations       *generations.Service
	logger            *slog.Logger
	customers         *customers.Service
	forwarding        *forwarding.Service
	groupNetworks     *groupconfig.Service
	ruleGroups        *rulegroups.Service
	siteConfig        *siteconfig.Service
	announcements     *announcements.Service
	vlessIdentity     *vlessidentity.Service
	vlessRuntime      *vlessruntime.Service
	vlessSetup        *vlesssetup.Service
	platformInfo      PlatformInfo
	usage             usagehttpapi.Service
	gatewayMembership *gatewaymembership.Handler
	diagnostics       *diagnostics.Service
	nezha             *nezhamonitor.Service
	targetProbe       *targetprobe.Service
}

func WithTargetProbe(service *targetprobe.Service) Option {
	return func(api *API) { api.targetProbe = service }
}

func WithNezha(service *nezhamonitor.Service) Option {
	return func(api *API) { api.nezha = service }
}

func WithDiagnostics(service *diagnostics.Service) Option {
	return func(api *API) { api.diagnostics = service }
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

func WithGatewayMembership(handler *gatewaymembership.Handler) Option {
	return func(api *API) { api.gatewayMembership = handler }
}

func WithVLESS(identityService *vlessidentity.Service, runtimeService *vlessruntime.Service) Option {
	return func(api *API) {
		api.vlessIdentity, api.vlessRuntime = identityService, runtimeService
	}
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
	mux.HandleFunc("GET /api/v1/auth/me", api.requireAdministrator(api.currentAdministrator))
	mux.HandleFunc("PUT /api/v1/auth/password", api.requireAdministrator(api.changeAdministratorPassword))
	mux.HandleFunc("GET /api/v1/overview", api.requireAdministrator(api.overview))
	mux.HandleFunc("GET /api/v1/nodes", api.requireAdministrator(api.listNodes))
	mux.HandleFunc("POST /api/v1/nodes/{node_id}/credential/rotate", api.requireAdministrator(api.rotateNodeCredential))
	if api.diagnostics != nil {
		mux.HandleFunc("GET /api/v1/diagnostics/targets", api.requireAdministrator(api.listDiagnosticTargets))
		mux.HandleFunc("POST /api/v1/diagnostics/run", api.requireAdministrator(api.runDiagnostic))
	}
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
	mux.HandleFunc("POST /api/v1/agent/heartbeat", api.requireNode(api.heartbeat))
	mux.HandleFunc("GET /api/v1/agent/desired", api.requireNode(api.desiredNodeConfig))
	mux.HandleFunc("POST /api/v1/agent/apply-results", api.requireNode(api.recordApplyResult))
	if api.vlessIdentity != nil {
		mux.HandleFunc("GET /api/v1/vless/identities", api.requireAdministrator(api.listVLESSIdentities))
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
	return session.AdminID, nil
}

type administratorHandler func(http.ResponseWriter, *http.Request, auth.Session)

func (api *API) requireAdministrator(next administratorHandler) http.HandlerFunc {
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
				api.logger.Error("http handler panic", "method", request.Method, "path", request.URL.Path)
				writeInternalProblem(writer)
			}
		}()
		next.ServeHTTP(writer, request)
	})
}
