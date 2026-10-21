package httpapi_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/panellicense"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestRestrictedLicenseRetainsExistingSubscriptionsAndAuthenticatedNodes(t *testing.T) {
	f := newBusinessFixture(t)
	var issued enrollment.IssueResult
	decodeResponse(t, requestJSON(t, f.handler, "POST", "/api/v1/enrollment-tokens", f.token, map[string]any{"name": "existing-edge", "expires_in_seconds": 900}, 201), &issued)
	enroll := agentv1.EnrollmentRequest{EnrollmentToken: issued.Token, Hostname: "isolated-edge", Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: []string{"xray"}}
	var node agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, "POST", "/api/v1/agent/enroll", "", enroll, 201), &node)
	f.handler = panellicense.Guard(panellicense.RestrictedClient(), f.handler)
	requestJSON(t, f.handler, "POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "isolated-test-administrator"}, 200)
	heartbeat := agentv1.HeartbeatRequest{BootID: "isolated-boot", Hostname: enroll.Hostname, Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: enroll.Capabilities}
	requestJSON(t, f.handler, "POST", "/api/v1/agent/heartbeat", node.NodeCredential, heartbeat, 204)
	requestJSON(t, f.handler, "POST", "/api/v1/agent/heartbeat", "", heartbeat, 401)
	requestJSON(t, f.handler, "GET", "/api/v1/agent/desired", node.NodeCredential, nil, 204)
	requestJSON(t, f.handler, "GET", "/api/v1/agent/desired", "", nil, 401)
	requestJSON(t, f.handler, "POST", "/api/v1/agent/enroll", "", enroll, 403)
	requestJSON(t, f.handler, "POST", "/api/v1/agent/update-task/claim", node.NodeCredential, nil, 403)
	requestJSON(t, f.handler, "DELETE", "/api/v1/customers/"+f.customer.ID, f.token, nil, 403)
	requestJSON(t, f.handler, "POST", "/api/v1/forward-rules", f.token, f.ruleRequest(), 403)
	requestJSON(t, f.handler, "GET", "/api/v1/customers", f.token, nil, 200)
	requestJSON(t, f.handler, "GET", "/api/v1/customers", "", nil, 401)

	resolver := &recoveringNativeResolver{healthy: true}
	record := subscriptions.Record{OwnerID: "admin", Item: subscriptions.Item{State: "active", ForwardingRuleID: "rule", CustomerID: forwarding.AdministratorSubjectID("admin")}, Published: []subscriptions.Line{{Name: "existing", BindingID: "binding"}}}
	original := httpapi.New(nil, nil, nil, nil, nil, nil, nil, httpapi.WithSubscriptions(subscriptions.NewService(publicFeedRepository{record: record}, resolver, nil)))
	restricted := panellicense.Guard(panellicense.RestrictedClient(), original)
	for _, suffix := range []string{".txt", ".yaml"} {
		path := "/api/v1/public/subscriptions/sub_" + strings.Repeat("a", 43) + suffix
		before, after := httptest.NewRecorder(), httptest.NewRecorder()
		original.ServeHTTP(before, httptest.NewRequest(http.MethodGet, path, nil))
		restricted.ServeHTTP(after, httptest.NewRequest(http.MethodGet, path, nil))
		if before.Code != 200 || after.Code != 200 || !bytes.Equal(before.Body.Bytes(), after.Body.Bytes()) {
			t.Fatal("license changed existing subscription URL or content")
		}
	}
}
