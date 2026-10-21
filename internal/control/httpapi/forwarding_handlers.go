package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
)

func (api *API) listForwardingRules(w http.ResponseWriter, r *http.Request, session auth.Session) {
	items, err := api.forwarding.ListForAdministrator(r.Context(), session.AdminID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) createCustomerForwardingRule(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input forwarding.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	item, replayed, err := api.forwarding.CreateForCustomerByAdministrator(r.Context(), session.AdminID, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	api.scheduleSavedVLESSRule(session.AdminID, item)
	writeBusinessResult(w, r, "rule", item, replayed)
}

func (api *API) getForwardingRule(w http.ResponseWriter, r *http.Request, session auth.Session) {
	item, err := api.forwarding.GetForAdministrator(r.Context(), session.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": item})
}

func (api *API) saveForwardingRule(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input forwarding.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	var item forwarding.Rule
	var replayed bool
	var err error
	if r.Method == http.MethodPost {
		item, replayed, err = api.forwarding.CreateForAdministrator(r.Context(), session.AdminID, input, r.Header.Get("Idempotency-Key"))
	} else {
		item, replayed, err = api.forwarding.UpdateForAdministrator(r.Context(), session.AdminID, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"))
	}
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	api.scheduleSavedVLESSRule(session.AdminID, item)
	writeBusinessResult(w, r, "rule", item, replayed)
}

func (api *API) scheduleSavedVLESSRule(administratorID string, item forwarding.Rule) {
	if api.vlessSetup != nil {
		api.vlessSetup.Schedule(administratorID, item, api.logger)
	}
}

func (api *API) listGroupNetworks(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	items, err := api.groupNetworks.List(r.Context())
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) getGroupNetwork(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	item, err := api.groupNetworks.Get(r.Context(), r.PathValue("group_id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"network": item})
}

func (api *API) saveGroupNetwork(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input groupconfig.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	item, replayed, err := api.groupNetworks.Update(r.Context(), session.AdminID, r.PathValue("group_id"), input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "network", item, replayed)
}
