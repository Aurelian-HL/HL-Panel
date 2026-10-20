package httpapi

import (
	"errors"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/panelquota"
	"github.com/hongle/hl-panel/internal/panellicense"
	"net/http"
)

func WithPanelLicense(client *panellicense.Client) Option {
	return func(api *API) { api.panelLicense = client }
}
func WithPanelLicenseUsage(source panelquota.UsageSource) Option {
	return func(api *API) { api.panelLicenseUsage = source }
}
func (api *API) writePanelLicenseStatus(w http.ResponseWriter, r *http.Request, actor string) {
	response := struct {
		panellicense.Status
		Usage            *panelquota.Usage `json:"usage,omitempty"`
		UsageUnavailable bool              `json:"usage_unavailable,omitempty"`
	}{Status: api.panelLicense.Status(actor)}
	if api.panelLicenseUsage != nil {
		usage, err := api.panelLicenseUsage.PanelResourceUsage(r.Context())
		if err == nil {
			response.Usage = &usage
		} else {
			response.UsageUnavailable = true
		}
	}
	writeJSON(w, http.StatusOK, response)
}
func (api *API) registerPanelLicenseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/license/status", api.requireAdministratorSession(func(w http.ResponseWriter, r *http.Request, s auth.Session) {
		if api.panelLicense == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		api.writePanelLicenseStatus(w, r, s.AdminID)
	}))
	if api.panelLicense == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/license/sync", api.requireAdministrator(func(w http.ResponseWriter, r *http.Request, s auth.Session) {
		if err := api.panelLicense.Sync(r.Context()); err != nil {
			if errors.Is(err, panellicense.ErrSyncThrottled) || errors.Is(err, panellicense.ErrSyncBusy) {
				w.Header().Set("Retry-After", "10")
				writeJSON(w, http.StatusTooManyRequests, problemEnvelope{Error: problem{Code: "license_sync_busy", Message: err.Error()}})
				return
			}
			panelLicenseFailure(w)
			return
		}
		api.writePanelLicenseStatus(w, r, s.AdminID)
	}))
	mux.HandleFunc("POST /api/v1/license/reminders/dismiss", api.requireAdministrator(func(w http.ResponseWriter, r *http.Request, s auth.Session) {
		if err := api.panelLicense.Dismiss(s.AdminID); err != nil {
			panelLicenseFailure(w)
			return
		}
		api.writePanelLicenseStatus(w, r, s.AdminID)
	}))
	mux.HandleFunc("POST /api/v1/license/free/prepare", api.requireAdministrator(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		challenge, err := api.panelLicense.RegisterFree(r.Context())
		if err != nil {
			panelLicenseFailure(w)
			return
		}
		writeJSON(w, http.StatusOK, challenge)
	}))
	mux.HandleFunc("POST /api/v1/license/activation/prepare", api.requireAdministrator(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var input struct {
			Code string `json:"code"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeProblem(w, r, err)
			return
		}
		challenge, err := api.panelLicense.Prepare(r.Context(), input.Code)
		if err != nil {
			panelLicenseFailure(w)
			return
		}
		writeJSON(w, http.StatusOK, challenge)
	}))
	mux.HandleFunc("POST /api/v1/license/activation/commit", api.requireAdministrator(func(w http.ResponseWriter, r *http.Request, s auth.Session) {
		if err := api.panelLicense.Commit(r.Context()); err != nil {
			panelLicenseFailure(w)
			return
		}
		api.writePanelLicenseStatus(w, r, s.AdminID)
	}))
}
func panelLicenseFailure(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadGateway, problemEnvelope{Error: problem{Code: "license_sync_failed", Message: "授权校验未完成，请检查授权码、DNS 证明或网络后重试；现有转发继续运行"}})
}
