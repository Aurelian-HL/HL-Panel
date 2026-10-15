package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/licensing"
)

type issueLicenseRequest struct {
	Domain         string `json:"domain"`
	Months         int    `json:"months"`
	IdempotencyKey string `json:"idempotency_key"`
}
type activateLicenseRequest struct {
	Code       string `json:"code"`
	Domain     string `json:"domain"`
	InstanceID string `json:"instance_id"`
}
type renewLicenseRequest struct {
	LicenseID  string `json:"license_id"`
	Code       string `json:"code"`
	Domain     string `json:"domain"`
	InstanceID string `json:"instance_id"`
}

func licenseProblem(w http.ResponseWriter, err error) {
	status, code, message := http.StatusBadRequest, "license_invalid", "授权码无效"
	switch {
	case errors.Is(err, licensing.ErrExpired):
		status, code, message = http.StatusForbidden, "license_expired", "授权已到期"
	case errors.Is(err, licensing.ErrRevoked):
		status, code, message = http.StatusForbidden, "license_revoked", "授权已撤销"
	case errors.Is(err, licensing.ErrBound):
		status, code, message = http.StatusConflict, "license_bound", "授权已绑定其他实例"
	case errors.Is(err, licensing.ErrDomainMismatch):
		status, code, message = http.StatusForbidden, "license_domain_mismatch", "域名与授权绑定不一致"
	case errors.Is(err, faults.ErrIdempotencyConflict):
		status, code, message = http.StatusConflict, "idempotency_conflict", "相同幂等键已用于不同授权请求"
	}
	writeJSON(w, status, problemEnvelope{Error: problem{Code: code, Message: message}})
}

func (api *API) issueLicenseCode(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input issueLicenseRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	bodyKey := strings.TrimSpace(input.IdempotencyKey)
	if headerKey == "" || (bodyKey != "" && bodyKey != headerKey) {
		writeProblem(w, r, faults.ErrValidation)
		return
	}
	item, raw, replayed, err := api.licensing.Issue(r.Context(), session.AdminID, licensing.IssueInput{Domain: input.Domain, Months: input.Months}, headerKey)
	if err != nil {
		licenseProblem(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"code": raw, "license": api.licensing.View(item), "replayed": replayed})
}

func (api *API) listLicenseCodes(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	items, err := api.licensing.List(r.Context())
	if err != nil {
		licenseProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (api *API) revokeLicenseCode(w http.ResponseWriter, r *http.Request, session auth.Session) {
	result, replayed, err := api.licensing.Revoke(r.Context(), session.AdminID, r.PathValue("id"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		licenseProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result, "replayed": replayed})
}
func (api *API) licenseStatus(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	items, err := api.licensing.List(r.Context())
	if err != nil {
		licenseProblem(w, err)
		return
	}
	lease, active, leaseErr := api.licensing.CurrentLease(r.Context())
	if leaseErr != nil && !errors.Is(leaseErr, licensing.ErrExpired) {
		licenseProblem(w, leaseErr)
		return
	}
	response := map[string]any{"items": items}
	if active {
		response["current"] = lease
	}
	writeJSON(w, http.StatusOK, response)
}
func (api *API) activateLicense(w http.ResponseWriter, r *http.Request) {
	var input activateLicenseRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	lease, err := api.licensing.Activate(r.Context(), input.Code, input.Domain, input.InstanceID)
	if err != nil {
		licenseProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}
func (api *API) renewLicense(w http.ResponseWriter, r *http.Request) {
	var input renewLicenseRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	lease, err := api.licensing.Renew(r.Context(), input.LicenseID, input.InstanceID, input.Code, input.Domain)
	if err != nil {
		licenseProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}
