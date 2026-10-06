package customerapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

func (api *API) ruleOptions(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	options, err := api.identity.RuleOptions(request.Context(), principal)
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, options)
}

func (api *API) createRule(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	var input forwarding.Request
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, err)
		return
	}
	rule, replayed, err := api.forwarding.CreateForCustomer(request.Context(), principal.CustomerID, input, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, map[string]any{"rule": rule, "replayed": replayed})
}

func (api *API) getRule(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	rule, err := api.forwarding.GetForCustomer(request.Context(), principal.CustomerID, request.PathValue("id"))
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"rule": rule})
}

func (api *API) updateRule(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	var input forwarding.Request
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, err)
		return
	}
	rule, replayed, err := api.forwarding.UpdateForCustomer(request.Context(), principal.CustomerID, request.PathValue("id"), input, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"rule": rule, "replayed": replayed})
}

func (api *API) batchRules(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	var input rulegroups.BatchRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, err)
		return
	}
	result, replayed, err := api.ruleGroups.BatchForCustomer(request.Context(), principal.CustomerID, input, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"result": result, "replayed": replayed})
}
