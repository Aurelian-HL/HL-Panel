package httpapi

import (
	"net/http"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
)

type createDeviceGroupRequest struct {
	Name            string      `json:"name"`
	Kind            groups.Kind `json:"kind"`
	UserGroupID     string      `json:"user_group_id"`
	HideInProbe     bool        `json:"hide_in_probe"`
	SelectionPolicy string      `json:"selection_policy"`
	Description     string      `json:"description"`
}

type updateDeviceGroupMemberWeightRequest struct {
	Weight    int       `json:"weight"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (api *API) updateDeviceGroupMemberWeight(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input updateDeviceGroupMemberWeightRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result, err := api.groups.UpdateMemberWeight(request.Context(), session.AdminID, request.PathValue("group_id"), request.PathValue("node_id"), input.Weight, input.UpdatedAt, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

type updateDeviceGroupRequest struct {
	Name            string `json:"name"`
	UserGroupID     string `json:"user_group_id"`
	HideInProbe     bool   `json:"hide_in_probe"`
	SelectionPolicy string `json:"selection_policy"`
	Description     string `json:"description"`
	Revision        int64  `json:"revision"`
}

func (api *API) updateDeviceGroup(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input updateDeviceGroupRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	group, replayed, err := api.groups.Update(request.Context(), session.AdminID, request.PathValue("group_id"), input.Name, input.UserGroupID, input.Description, input.HideInProbe, endpoints.SelectionPolicy(input.SelectionPolicy), input.Revision, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "group", group, replayed)
}

func (api *API) deleteDeviceGroup(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	replayed, err := api.groups.Delete(request.Context(), session.AdminID, request.PathValue("group_id"), request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Replayed bool `json:"replayed"`
	}{Replayed: replayed})
}

type addDeviceGroupMemberRequest struct {
	NodeID   string `json:"node_id"`
	DialHost string `json:"dial_host"`
	Weight   int    `json:"weight"`
	Priority int    `json:"priority"`
}

func (api *API) createDeviceGroup(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input createDeviceGroupRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	group, err := api.groups.CreateWithMetadata(request.Context(), session.AdminID, input.Name, input.Kind, endpoints.SelectionPolicy(input.SelectionPolicy), input.Description, groups.CreateMetadata{UserGroupID: input.UserGroupID, HideInProbe: input.HideInProbe})
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, struct {
		Group groups.DeviceGroup `json:"group"`
	}{Group: group})
}

func (api *API) addDeviceGroupMember(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input addDeviceGroupMemberRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	member, assignments, err := api.groups.AddMemberWithDialHost(request.Context(), session.AdminID, request.PathValue("group_id"), input.NodeID, input.DialHost, input.Weight, input.Priority)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Member      groups.Member                      `json:"member"`
		Assignments []generations.NodeConfigGeneration `json:"assignments"`
	}{Member: member, Assignments: assignments})
}

func (api *API) listDeviceGroupMembers(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	members, err := api.groups.ListMembers(request.Context(), request.PathValue("group_id"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []groups.Member `json:"items"`
	}{Items: members})
}

func (api *API) retireDeviceGroupMember(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	result, err := api.groups.RetireMember(request.Context(), session.AdminID, request.PathValue("group_id"), request.PathValue("node_id"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
