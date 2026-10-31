package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/panelquota"
	"github.com/hongle/hl-panel/internal/panellicense"
	"io"
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
	mux.HandleFunc("GET /api/v1/license/shop", api.requireAdministrator(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		products, err := api.panelLicense.ShopCatalog(r.Context())
		if errors.Is(err, panellicense.ErrShopNotOpen) {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"items": panellicense.DefaultShopProducts(), "redemption_enabled": false, "message": "商店尚未开通，暂不支持购买或兑换。"})
			return
		}
		if err != nil {
			panelShopFailure(w, err)
			return
		}
		if products == nil {
			products = []panellicense.ShopProduct{}
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"items": products, "redemption_enabled": true})
	}))
	mux.HandleFunc("POST /api/v1/license/redemption", api.requireAdministrator(func(w http.ResponseWriter, r *http.Request, s auth.Session) {
		code, err := decodePanelLicenseCode(w, r)
		if err != nil {
			writeProblem(w, r, err)
			return
		}
		if err := api.panelLicense.Redeem(r.Context(), code); err != nil {
			panelShopFailure(w, err)
			return
		}
		api.writePanelLicenseStatus(w, r, s.AdminID)
	}))
	mux.HandleFunc("POST /api/v1/license/redemption/resume", api.requireAdministrator(func(w http.ResponseWriter, r *http.Request, s auth.Session) {
		if err := api.panelLicense.ResumeRedemption(r.Context()); err != nil {
			panelShopFailure(w, err)
			return
		}
		api.writePanelLicenseStatus(w, r, s.AdminID)
	}))
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
		code, err := decodePanelLicenseCode(w, r)
		if err != nil {
			writeProblem(w, r, err)
			return
		}
		challenge, err := api.panelLicense.Prepare(r.Context(), code)
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

// Decoder diagnostics can include an unknown field name copied from a secret.
// Keep these small, sensitive requests separate from general JSON error output.
func decodePanelLicenseCode(w http.ResponseWriter, r *http.Request) (string, error) {
	var input struct {
		Code string `json:"code"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	invalid := func() (string, error) {
		return "", fmt.Errorf("%w: 授权码请求无效，请重新填写并提交", faults.ErrValidation)
	}
	if err := decoder.Decode(&input); err != nil {
		return invalid()
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid()
	}
	return input.Code, nil
}

func panelLicenseFailure(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadGateway, problemEnvelope{Error: problem{Code: "license_sync_failed", Message: "授权校验未完成，请检查授权码、DNS 证明或网络后重试；现有转发继续运行"}})
}

func panelShopFailure(w http.ResponseWriter, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(err, panellicense.ErrSyncBusy) {
		w.Header().Set("Retry-After", "10")
		writeJSON(w, http.StatusTooManyRequests, problemEnvelope{Error: problem{Code: "license_sync_busy", Message: "授权操作正在进行，请稍后重试"}})
		return
	}
	var safe *panellicense.UserError
	if errors.As(err, &safe) {
		status := http.StatusConflict
		if safe.Code == "license_service_unavailable" {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, problemEnvelope{Error: problem{Code: safe.Code, Message: safe.Message}})
		return
	}
	writeJSON(w, http.StatusBadGateway, problemEnvelope{Error: problem{Code: "license_shop_unavailable", Message: "商店或兑换结果暂未确认，请稍后重试；已提交的兑换请恢复结果"}})
}
