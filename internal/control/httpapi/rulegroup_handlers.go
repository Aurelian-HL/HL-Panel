package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

func (api *API) listRuleGroups(w http.ResponseWriter, r *http.Request, session auth.Session) {
	items, err := api.ruleGroups.ListForAdministrator(r.Context(), session.AdminID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) getRuleGroup(w http.ResponseWriter, r *http.Request, session auth.Session) {
	item, err := api.ruleGroups.GetForAdministrator(r.Context(), session.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule_group": item})
}

func (api *API) saveRuleGroup(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input rulegroups.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	var item rulegroups.RuleGroup
	var replayed bool
	var err error
	if r.Method == http.MethodPost {
		item, replayed, err = api.ruleGroups.Create(r.Context(), session.AdminID, input, r.Header.Get("Idempotency-Key"))
	} else {
		item, replayed, err = api.ruleGroups.Update(r.Context(), session.AdminID, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"))
	}
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "rule_group", item, replayed)
}

func (api *API) batchForwardingRules(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input rulegroups.BatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	result, replayed, err := api.ruleGroups.BatchForAdministrator(r.Context(), session.AdminID, input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result, "replayed": replayed})
}
