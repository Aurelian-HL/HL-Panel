package httpapi

import (
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"net/http"
)

func (api *API) generateRuleSubscription(w http.ResponseWriter, r *http.Request, s auth.Session) {
	subscriptionHeaders(w)
	rule, err := api.forwarding.GetForAdministrator(r.Context(), s.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	credential, err := api.vlessIdentity.CredentialForAdministrator(r.Context(), s.AdminID, rule.ID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	item, replayed, err := api.subscriptions.Generate(r.Context(), s.AdminID, rule.ID, subscriptions.Request{Name: rule.Name, CustomerID: rule.CustomerID, Lines: []subscriptions.Line{{Name: rule.Name, BindingID: credential.Binding.ID}}}, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscription": item, "replayed": replayed})
}
