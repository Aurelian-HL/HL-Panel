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
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("refresh") == "true" {
		writeJSON(w, http.StatusOK, api.releases.Refresh(r.Context()))
		return
	}
	writeJSON(w, http.StatusOK, api.releases.Check(r.Context()))
}
