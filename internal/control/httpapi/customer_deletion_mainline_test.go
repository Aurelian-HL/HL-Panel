package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/hongle/hl-panel/internal/control/customers"
)

func TestMainlineCustomerAndUserGroupDeletionAuthorizationValidationAndReplay(t *testing.T) {
	f := newBusinessFixture(t)
	path := "/api/v1/customers/" + f.customer.ID
	request := customers.DeleteRequest{Revision: f.customer.Revision}
	requestJSON(t, f.handler, http.MethodDelete, path, "", request, http.StatusUnauthorized)
	businessRequest(t, f, http.MethodDelete, path, "", request, http.StatusBadRequest)
	businessRequest(t, f, http.MethodDelete, path, "delete-customer", customers.DeleteRequest{Revision: request.Revision + 1}, http.StatusConflict)
	groupPath := "/api/v1/user-groups/" + f.userGroup.ID
	groupRequest := customers.DeleteRequest{Revision: f.userGroup.Revision}
	businessRequest(t, f, http.MethodDelete, groupPath, "delete-user-group", groupRequest, http.StatusConflict)
	before := len(f.store.AuditEvents())
	businessRequest(t, f, http.MethodDelete, path, "delete-customer", request, http.StatusOK)
	var replay struct {
		Replayed bool `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodDelete, path, "delete-customer", request, http.StatusOK), &replay)
	if !replay.Replayed || len(f.store.AuditEvents()) != before+1 {
		t.Fatal("delete replay appended audit or was not replayed")
	}
	businessRequest(t, f, http.MethodGet, path, "", nil, http.StatusNotFound)
	businessRequest(t, f, http.MethodDelete, groupPath, "delete-user-group", groupRequest, http.StatusOK)
	businessRequest(t, f, http.MethodGet, groupPath, "", nil, http.StatusNotFound)
}
