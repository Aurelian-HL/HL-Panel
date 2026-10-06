package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nezhamonitor"
)

func (api *API) listNezhaGroupMonitoring(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	groupID := request.PathValue("group_id")
	groups, err := api.groups.List(request.Context())
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	found := false
	for _, group := range groups {
		if group.ID == groupID {
			found = true
			break
		}
	}
	if !found {
		writeProblem(writer, request, fmt.Errorf("%w: group", faults.ErrNotFound))
		return
	}
	members, err := api.groups.ListMembers(request.Context(), groupID)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	nodeIDs := make([]string, 0, len(members))
	for _, member := range members {
		if member.RetiredAt == nil {
			nodeIDs = append(nodeIDs, member.NodeID)
		}
	}
	items := nezhamonitor.Unlinked(nodeIDs)
	status := "disabled"
	if api.nezha != nil {
		status = "ok"
		items, err = api.nezha.Fetch(request.Context(), nodeIDs)
		if err != nil {
			status = "unavailable"
			// Fetch retains explicit links, but never exposes upstream errors or credentials.
		}
	}
	views, err := api.nodes.List(request.Context())
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	selected := views[:0]
	for _, view := range views {
		for _, id := range nodeIDs {
			if view.ID == id {
				selected = append(selected, view)
				break
			}
		}
	}
	items = nezhamonitor.MergeNative(items, selected, time.Now())
	writeJSON(writer, http.StatusOK, struct {
		Items          []nezhamonitor.Item `json:"items"`
		UpstreamStatus string              `json:"upstream_status"`
	}{Items: items, UpstreamStatus: status})
}

func (api *API) listNezhaInventory(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	items := []nezhamonitor.Item{}
	status := "disabled"
	if api.nezha != nil {
		status = "ok"
		var err error
		items, err = api.nezha.FetchInventory(request.Context())
		if err != nil {
			status = "unavailable"
			items = []nezhamonitor.Item{}
		}
	}
	views, err := api.nodes.List(request.Context())
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	items = nezhamonitor.MergeNative(items, views, time.Now())
	writeJSON(writer, http.StatusOK, struct {
		Items          []nezhamonitor.Item `json:"items"`
		UpstreamStatus string              `json:"upstream_status"`
	}{Items: items, UpstreamStatus: status})
}
