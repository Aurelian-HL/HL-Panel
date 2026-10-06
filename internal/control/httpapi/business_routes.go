package httpapi

import "net/http"

func (api *API) registerBusinessRoutes(mux *http.ServeMux) {
	if api.customers != nil {
		mux.HandleFunc("GET /api/v1/customers", api.requireAdministrator(api.listCustomers))
		mux.HandleFunc("GET /api/v1/customers/{id}", api.requireAdministrator(api.getCustomer))
		mux.HandleFunc("POST /api/v1/customers", api.requireAdministrator(api.saveCustomer))
		mux.HandleFunc("PUT /api/v1/customers/{id}", api.requireAdministrator(api.saveCustomer))
		mux.HandleFunc("GET /api/v1/user-groups", api.requireAdministrator(api.listUserGroups))
		mux.HandleFunc("GET /api/v1/user-groups/{id}", api.requireAdministrator(api.getUserGroup))
		mux.HandleFunc("POST /api/v1/user-groups", api.requireAdministrator(api.saveUserGroup))
		mux.HandleFunc("PUT /api/v1/user-groups/{id}", api.requireAdministrator(api.saveUserGroup))
	}
	if api.forwarding != nil {
		mux.HandleFunc("GET /api/v1/forwarding-rules", api.requireAdministrator(api.listForwardingRules))
		mux.HandleFunc("GET /api/v1/forwarding-rules/export", api.requireAdministrator(api.exportForwardingRules))
		mux.HandleFunc("POST /api/v1/forwarding-rules/import/preview", api.requireAdministrator(api.previewForwardingImport))
		mux.HandleFunc("POST /api/v1/forwarding-rules/import", api.requireAdministrator(api.importForwardingRules))
	mux.HandleFunc("GET /api/v1/forwarding-rules/{id}", api.requireAdministrator(api.getForwardingRule))
	mux.HandleFunc("GET /api/v1/forwarding-rules/{id}/connection", api.requireAdministrator(api.getForwardingRuleConnection))
		mux.HandleFunc("POST /api/v1/forwarding-rules", api.requireAdministrator(api.saveForwardingRule))
		mux.HandleFunc("PUT /api/v1/forwarding-rules/{id}", api.requireAdministrator(api.saveForwardingRule))
	}
	if api.targetProbe != nil {
		mux.HandleFunc("POST /api/v1/forwarding-target-probes", api.requireAdministrator(api.probeForwardingTarget))
	}
	if api.ruleGroups != nil {
		mux.HandleFunc("GET /api/v1/rule-groups", api.requireAdministrator(api.listRuleGroups))
		mux.HandleFunc("GET /api/v1/rule-groups/{id}", api.requireAdministrator(api.getRuleGroup))
		mux.HandleFunc("POST /api/v1/rule-groups", api.requireAdministrator(api.saveRuleGroup))
		mux.HandleFunc("PUT /api/v1/rule-groups/{id}", api.requireAdministrator(api.saveRuleGroup))
		mux.HandleFunc("POST /api/v1/forwarding-rules/batch", api.requireAdministrator(api.batchForwardingRules))
	}
	if api.groupNetworks != nil {
		mux.HandleFunc("GET /api/v1/group-networks", api.requireAdministrator(api.listGroupNetworks))
		mux.HandleFunc("GET /api/v1/group-networks/{group_id}", api.requireAdministrator(api.getGroupNetwork))
		mux.HandleFunc("PUT /api/v1/group-networks/{group_id}", api.requireAdministrator(api.saveGroupNetwork))
	}
}

func writeBusinessResult(writer http.ResponseWriter, request *http.Request, key string, item any, replayed bool) {
	status := http.StatusOK
	if request.Method == http.MethodPost && !replayed {
		status = http.StatusCreated
	}
	writeJSON(writer, status, map[string]any{key: item, "replayed": replayed})
}
