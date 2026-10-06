package httpapi

import (
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"net/http"
)

func (api *API) listCustomers(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	items, err := api.customers.ListCustomers(r.Context())
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) getCustomer(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	item, err := api.customers.Customer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"customer": item})
}

func (api *API) saveCustomer(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input customers.CustomerInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	var item customers.Customer
	var replayed bool
	var err error
	if r.Method == http.MethodPost {
		item, replayed, err = api.customers.CreateCustomer(r.Context(), session.AdminID, input)
	} else {
		item, replayed, err = api.customers.UpdateCustomer(r.Context(), session.AdminID, r.PathValue("id"), input)
	}
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "customer", item, replayed)
}

func (api *API) listUserGroups(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	items, err := api.customers.ListUserGroups(r.Context())
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) getUserGroup(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	item, err := api.customers.UserGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_group": item})
}

func (api *API) saveUserGroup(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input customers.UserGroupInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	var item customers.UserGroup
	var replayed bool
	var err error
	if r.Method == http.MethodPost {
		item, replayed, err = api.customers.CreateUserGroup(r.Context(), session.AdminID, input)
	} else {
		item, replayed, err = api.customers.UpdateUserGroup(r.Context(), session.AdminID, r.PathValue("id"), input)
	}
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "user_group", item, replayed)
}
