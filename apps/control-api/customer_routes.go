package main

import (
	"log/slog"
	"net/http"

	"github.com/hongle/hl-panel/internal/control/customerapi"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
)

type customerIdentityRepositoryProvider interface {
	CustomerIdentityRepository(panelVersion string) customeridentity.Repository
}

// newCustomerAPIHandler is intentionally separate from the administrator API.
// The caller must provide a dedicated secret loaded from protected runtime
// configuration; the administrator bootstrap password is not an acceptable key.
func newCustomerAPIHandler(provider customerIdentityRepositoryProvider, panelVersion string, passwordFingerprintKey []byte, logger *slog.Logger, options ...customerapi.Option) (http.Handler, error) {
	identity, err := customeridentity.NewService(provider.CustomerIdentityRepository(panelVersion), nil, customeridentity.Options{
		PasswordFingerprintKey: passwordFingerprintKey,
	})
	if err != nil {
		return nil, err
	}
	return customerapi.New(identity, logger, options...), nil
}

// mountCustomerRoutes gives the customer surface ownership of its namespace
// and sends every other route to the existing administrator/agent handler.
func mountCustomerRoutes(controlHandler, customerHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/customer/", customerHandler)
	mux.Handle("/", controlHandler)
	return mux
}
