package httpapi

import (
	"encoding/base64"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"net/http"
	"strings"
)

func (api *API) registerSubscriptionRoutes(mux *http.ServeMux) {
	if api.subscriptions == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/forwarding-rules/{id}/subscription", api.requireAdministrator(api.generateRuleSubscription))
	mux.HandleFunc("GET /api/v1/subscriptions", api.requireAdministrator(api.listSubscriptions))
	mux.HandleFunc("POST /api/v1/subscriptions", api.requireAdministrator(api.saveSubscription))
	mux.HandleFunc("GET /api/v1/subscriptions/{id}", api.requireAdministrator(api.subscriptionDetail))
	mux.HandleFunc("PUT /api/v1/subscriptions/{id}", api.requireAdministrator(api.saveSubscription))
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/actions/{action}", api.requireAdministrator(api.subscriptionAction))
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/package", api.requireAdministrator(api.subscriptionPackage))
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/qrcode", api.requireAdministrator(api.subscriptionQRCode))
	mux.HandleFunc("GET /api/v1/public/subscriptions/{token}", api.publicSubscription)
}
func safeRequestPath(path string) string {
	if strings.HasPrefix(path, "/api/v1/public/subscriptions/") {
		return "/api/v1/public/subscriptions/[redacted]"
	}
	return path
}
func subscriptionHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
}
func (api *API) listSubscriptions(w http.ResponseWriter, r *http.Request, s auth.Session) {
	items, err := api.subscriptions.List(r.Context(), s.AdminID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (api *API) subscriptionDetail(w http.ResponseWriter, r *http.Request, s auth.Session) {
	subscriptionHeaders(w)
	record, err := api.subscriptions.Detail(r.Context(), s.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	draft, draftErr := api.subscriptions.Resolve(r.Context(), record, true)
	published, publishedErr := api.subscriptions.Resolve(r.Context(), record, false)
	warning := ""
	if draftErr != nil || publishedErr != nil {
		warning = "客户已停用、到期或额度不足，订阅暂不可用"
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscription": record.Item, "lines": record.Draft, "preview": draft, "published_preview": published, "links": subscriptions.Paths(record.Token), "warning": warning})
}
func (api *API) saveSubscription(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var input subscriptions.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	operation := "update"
	if r.Method == http.MethodPost {
		operation = "create"
	}
	item, replayed, err := api.subscriptions.Mutate(r.Context(), s.AdminID, r.PathValue("id"), operation, input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscription": item, "replayed": replayed})
}
func (api *API) subscriptionAction(w http.ResponseWriter, r *http.Request, s auth.Session) {
	var input vlessRevisionRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	item, replayed, err := api.subscriptions.Mutate(r.Context(), s.AdminID, r.PathValue("id"), r.PathValue("action"), subscriptions.Request{Revision: input.Revision}, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscription": item, "replayed": replayed})
}
func (api *API) publicSubscription(w http.ResponseWriter, r *http.Request) {
	subscriptionHeaders(w)
	token := r.PathValue("token")
	format := "txt"
	if strings.HasSuffix(token, ".yaml") {
		format = "yaml"
		token = strings.TrimSuffix(token, ".yaml")
	} else {
		token = strings.TrimSuffix(token, ".txt")
	}
	_, lines, err := api.subscriptions.Public(r.Context(), token)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	w.Header().Set("profile-update-interval", "1")
	if format == "yaml" {
		content, err := subscriptions.YAML(lines)
		if err != nil {
			writeProblem(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(content)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(subscriptions.TXT(lines)))
	}
}
func (api *API) subscriptionPackage(w http.ResponseWriter, r *http.Request, s auth.Session) {
	subscriptionHeaders(w)
	var input struct {
		BaseURL string `json:"base_url"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	record, err := api.subscriptions.Detail(r.Context(), s.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	if record.Item.State != "active" || record.Item.PublishedRevision == 0 {
		writeProblem(w, r, faults.ErrConflict)
		return
	}
	lines, err := api.subscriptions.Resolve(r.Context(), record, false)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	content, err := subscriptions.Package(record, lines, input.BaseURL)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filename": subscriptions.PackageBaseName(record.Item.Name) + "-订阅导入包.zip", "data_base64": base64.StdEncoding.EncodeToString(content)})
}

func (api *API) subscriptionQRCode(w http.ResponseWriter, r *http.Request, s auth.Session) {
	subscriptionHeaders(w)
	var input struct {
		BaseURL string `json:"base_url"`
		Format  string `json:"format"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	record, err := api.subscriptions.Detail(r.Context(), s.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	if record.Item.State != "active" || record.Item.PublishedRevision == 0 {
		writeProblem(w, r, faults.ErrConflict)
		return
	}
	if _, err := api.subscriptions.Resolve(r.Context(), record, false); err != nil {
		writeProblem(w, r, err)
		return
	}
	content, err := subscriptions.QRCode(record.Token, input.BaseURL, input.Format)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"png_base64": base64.StdEncoding.EncodeToString(content)})
}
