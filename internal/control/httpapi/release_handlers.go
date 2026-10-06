package httpapi

import (
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/releases"
	"net/http"
)

func WithReleases(service *releases.Service) Option {
	return func(api *API) { api.releases = service }
}

func (api *API) checkVersion(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, api.releases.Check(r.Context()))
}
