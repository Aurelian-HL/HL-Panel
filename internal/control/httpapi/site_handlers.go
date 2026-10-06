package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
)

func (api *API) registerSiteRoutes(mux *http.ServeMux) {
	if api.siteConfig == nil || api.announcements == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/public/site-info", api.publicSiteInfo)
	mux.HandleFunc("GET /api/v1/site-settings", api.requireAdministrator(api.getSiteSettings))
	mux.HandleFunc("PUT /api/v1/site-settings", api.requireAdministrator(api.saveSiteSettings))
	mux.HandleFunc("GET /api/v1/announcements", api.requireAdministrator(api.listAnnouncements))
	mux.HandleFunc("POST /api/v1/announcements", api.requireAdministrator(api.saveAnnouncement))
	mux.HandleFunc("PUT /api/v1/announcements/{id}", api.requireAdministrator(api.saveAnnouncement))
}

func (api *API) publicSiteInfo(w http.ResponseWriter, r *http.Request) {
	settings, err := api.siteConfig.Get(r.Context())
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	items, err := api.announcements.Active(r.Context())
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	buildTime := any(nil)
	if api.platformInfo.BuildTime != "" {
		buildTime = api.platformInfo.BuildTime
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings, "announcements": items,
		"platform_version": api.platformInfo.Version, "build_time": buildTime,
	})
}

func (api *API) getSiteSettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	settings, err := api.siteConfig.Get(r.Context())
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

func (api *API) saveSiteSettings(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input siteconfig.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	settings, replayed, err := api.siteConfig.Update(r.Context(), session.AdminID, input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "settings", settings, replayed)
}

func (api *API) listAnnouncements(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	items, err := api.announcements.List(r.Context())
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) saveAnnouncement(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input announcements.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	var item announcements.Announcement
	var replayed bool
	var err error
	if r.Method == http.MethodPost {
		item, replayed, err = api.announcements.Create(r.Context(), session.AdminID, input, r.Header.Get("Idempotency-Key"))
	} else {
		item, replayed, err = api.announcements.Update(r.Context(), session.AdminID, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"))
	}
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "announcement", item, replayed)
}
