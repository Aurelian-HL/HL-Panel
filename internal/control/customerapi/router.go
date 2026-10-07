// Package customerapi exposes the customer self-service HTTP surface. It is a
// separate handler from the administrator API so customer sessions can never
// satisfy administrator middleware by accident.
package customerapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

var errInternal = errors.New("internal error")

type API struct {
	identity    *customeridentity.Service
	forwarding  *forwarding.Service
	ruleGroups  *rulegroups.Service
	connections *ConnectionService
	logger      *slog.Logger
}

type Option func(*API)

func WithRuleWrites(forwardingService *forwarding.Service, ruleGroupService *rulegroups.Service) Option {
	return func(api *API) {
		api.forwarding, api.ruleGroups = forwardingService, ruleGroupService
	}
}

func WithConnectionReader(reader ConnectionReader) Option {
	return func(api *API) { api.connections = NewConnectionService(api.identity, reader, nil) }
}

func New(identity *customeridentity.Service, logger *slog.Logger, options ...Option) http.Handler {
	if identity == nil {
		panic("customer identity service is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	api := &API{identity: identity, logger: logger}
	for _, option := range options {
		option(api)
	}
	if api.connections == nil {
		api.connections = NewConnectionService(identity, nil, nil)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/customer/auth/login", api.login)
	mux.HandleFunc("POST /api/v1/customer/auth/logout", api.requireCustomer(api.logout))
	mux.HandleFunc("GET /api/v1/customer/portal", api.portal)
	mux.HandleFunc("GET /api/v1/customer/me", api.requireCustomer(api.me))
	mux.HandleFunc("PUT /api/v1/customer/password", api.requireCustomer(api.changePassword))
	mux.HandleFunc("GET /api/v1/customer/rules", api.requireCustomer(api.rules))
	mux.HandleFunc("GET /api/v1/customer/rule-options", api.requireCustomer(api.ruleOptions))
	if api.forwarding != nil && api.ruleGroups != nil {
		mux.HandleFunc("POST /api/v1/customer/rules", api.requireCustomer(api.createRule))
		mux.HandleFunc("GET /api/v1/customer/rules/{id}", api.requireCustomer(api.getRule))
		mux.HandleFunc("PUT /api/v1/customer/rules/{id}", api.requireCustomer(api.updateRule))
		mux.HandleFunc("POST /api/v1/customer/rules/batch", api.requireCustomer(api.batchRules))
	}
	mux.HandleFunc("GET /api/v1/customer/usage", api.requireCustomer(api.usage))
	mux.HandleFunc("GET /api/v1/customer/connections", api.requireCustomer(api.connectionsView))
	mux.HandleFunc("GET /api/v1/customer/subscriptions", api.requireCustomer(api.subscriptionsView))
	return api.securityHeaders(api.recoverPanics(mux))
}

type customerHandler func(http.ResponseWriter, *http.Request, customeridentity.Principal)

func (api *API) requireCustomer(next customerHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		principal, err := api.identity.Authenticate(request.Context(), bearerToken(request.Header.Get("Authorization")))
		if err != nil {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(writer, err)
			return
		}
		next(writer, request, principal)
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
				api.logger.Error("customer API panic", "method", request.Method, "path", request.URL.Path)
				writeProblem(writer, errInternal)
			}
		}()
		next.ServeHTTP(writer, request)
	})
}
