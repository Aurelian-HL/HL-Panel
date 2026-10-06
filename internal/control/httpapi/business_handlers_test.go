package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/targetprobe"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type businessFixture struct {
	handler   http.Handler
	token     string
	store     *memoryrepo.Store
	entry     groups.DeviceGroup
	exit      groups.DeviceGroup
	userGroup customers.UserGroup
	customer  customers.Customer
}

func newBusinessFixture(t *testing.T) businessFixture {
	t.Helper()
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	hash, err := auth.HashPassword("isolated-test-administrator")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm_business", Username: "admin", PasswordHash: hash, CreatedAt: now})
	vlessIdentity, err := vlessidentity.NewService(store, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	vlessRuntime, err := vlessruntime.NewService(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(auth.NewService(store, audit.NewService(store), clock, time.Hour), enrollment.NewService(store, clock, time.Hour), nodes.NewService(store, clock, time.Minute), groups.NewService(store, clock), endpoints.NewService(store, clock), generations.NewService(store, clock), slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.WithBusiness(customers.NewService(store, clock), forwarding.NewService(store, clock), groupconfig.NewService(store, clock)), httpapi.WithRuleGroups(rulegroups.NewService(store, clock)), httpapi.WithTargetProbe(targetprobe.New(time.Second)), httpapi.WithVLESS(vlessIdentity, vlessRuntime))
	var login auth.LoginResult
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "isolated-test-administrator"}, http.StatusOK), &login)
	f := businessFixture{handler: handler, token: login.AccessToken, store: store}
	f.entry = createGroup(t, handler, f.token, "入口设备组", groups.KindEntry)
	f.exit = createGroup(t, handler, f.token, "出口设备组", groups.KindExit)
	putNetwork := groupconfig.Request{ConnectHost: "entry.example.test", PortStart: 20000, PortEnd: 20009, AllowDirect: true, AllowedExitGroupIDs: []string{f.exit.ID}, TrafficMultiplier: 1}
	businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "entry-network-initial", putNetwork, http.StatusOK)
	groupBody := businessRequest(t, f, http.MethodPost, "/api/v1/user-groups", "user-group-initial", customers.UserGroupInput{Name: "测试用户组", AllowedEntryGroupIDs: []string{f.entry.ID}, AllowedExitGroupIDs: []string{f.exit.ID}, AllowDirect: true}, http.StatusCreated)
	decodeBusinessEntity(t, groupBody, "user_group", &f.userGroup)
	customerBody := businessRequest(t, f, http.MethodPost, "/api/v1/customers", "customer-initial", customers.CustomerInput{Username: "demo-user", DisplayName: "业务测试客户", UserGroupID: f.userGroup.ID, Password: "12345678", MaxRules: 3, TrafficLimitBytes: 1024}, http.StatusCreated)
	decodeBusinessEntity(t, customerBody, "customer", &f.customer)
	if bytes.Contains(customerBody, []byte("12345678")) || bytes.Contains(customerBody, []byte("password_hash")) {
		t.Fatal("customer mutation leaked a password")
	}
	return f
}

func TestForwardingTargetProbeAcceptsOnlyHostAndPort(t *testing.T) {
	f := newBusinessFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
	}()
	_, rawPort, _ := net.SplitHostPort(listener.Addr().String())
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Probe struct {
			Reachable bool   `json:"reachable"`
			Address   string `json:"address"`
		} `json:"probe"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-target-probes", "probe-loopback", targetprobe.Request{Host: "127.0.0.1", Port: port}, http.StatusOK), &response)
	if !response.Probe.Reachable || response.Probe.Address != listener.Addr().String() {
		t.Fatalf("unexpected probe response: %+v", response)
	}
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-target-probes", "probe-credentials", targetprobe.Request{Host: "socks5://user:pass@127.0.0.1", Port: port}, http.StatusBadRequest)
}

func (f businessFixture) ruleRequest() forwarding.Request {
	return forwarding.Request{Name: "客户直连", CustomerID: f.customer.ID, EntryGroupID: f.entry.ID, EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 9000}, {Host: "::1", Port: 9001}}, SelectionPolicy: forwarding.SelectionRoundRobin}
}

func businessRequest(t *testing.T, f businessFixture, method, path, key string, body any, status int) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer "+f.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != status {
		t.Fatalf("%s %s status %d want %d: %s", method, path, recorder.Code, status, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

func decodeBusinessEntity(t *testing.T, body []byte, key string, out any) {
	t.Helper()
	var envelope map[string]json.RawMessage
	decodeResponse(t, body, &envelope)
	raw, ok := envelope[key]
	if !ok {
		t.Fatalf("missing %s response entity: %s", key, body)
	}
	decodeResponse(t, raw, out)
}

func TestBusinessForwardingLifecycleAndIdempotency(t *testing.T) {
	f := newBusinessFixture(t)
	input := f.ruleRequest()
	var rule forwarding.Rule
	created := businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "create-forwarding-1", input, http.StatusCreated)
	decodeBusinessEntity(t, created, "rule", &rule)
	if rule.ID == "" || rule.ListenPort != 20000 || rule.Revision != 1 || rule.Status != forwarding.StatusPendingActivation || rule.Deployed {
		t.Fatalf("unexpected saved rule: %+v", rule)
	}
	auditCount := len(f.store.AuditEvents())
	var replay struct {
		Rule     forwarding.Rule `json:"rule"`
		Replayed bool            `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "create-forwarding-1", input, http.StatusOK), &replay)
	if !replay.Replayed || replay.Rule.ID != rule.ID || replay.Rule.ListenPort != rule.ListenPort {
		t.Fatal("create replay allocated another rule or port")
	}
	if len(f.store.AuditEvents()) != auditCount {
		t.Fatal("create replay duplicated its audit event")
	}
	changed := input
	changed.Name = "不同请求"
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "create-forwarding-1", changed, http.StatusConflict)
	input.Revision = rule.Revision
	input.ListenPort = rule.ListenPort
	input.Paused = true
	var paused forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/forwarding-rules/"+rule.ID, "pause-forwarding-1", input, http.StatusOK), "rule", &paused)
	if paused.Status != forwarding.StatusPaused || paused.Revision != 2 || !paused.CreatedAt.Equal(rule.CreatedAt) || paused.Deployed {
		t.Fatalf("pause did not preserve the lifecycle: %+v", paused)
	}
	auditCount = len(f.store.AuditEvents())
	decodeResponse(t, businessRequest(t, f, http.MethodPut, "/api/v1/forwarding-rules/"+rule.ID, "pause-forwarding-1", input, http.StatusOK), &replay)
	if !replay.Replayed || replay.Rule.Revision != 2 {
		t.Fatal("PUT replay must succeed after the revision advanced")
	}
	if len(f.store.AuditEvents()) != auditCount {
		t.Fatal("PUT replay duplicated its audit event")
	}
	input.Paused = false
	businessRequest(t, f, http.MethodPut, "/api/v1/forwarding-rules/"+rule.ID, "stale-forwarding-1", input, http.StatusConflict)
	input.Revision = paused.Revision
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/forwarding-rules/"+rule.ID, "resume-forwarding-1", input, http.StatusOK), "rule", &rule)
	if rule.Status != forwarding.StatusPendingActivation || rule.Deployed || rule.Paused || rule.Revision != 3 {
		t.Fatal("resume must remain pending until verified engine activation")
	}
	var listed struct {
		Items []forwarding.Rule `json:"items"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules", "", nil, http.StatusOK), &listed)
	if len(listed.Items) != 1 || listed.Items[0].Revision != 3 {
		t.Fatal("list did not reflect actual mutations")
	}
	stored, err := f.store.Customer(context.Background(), f.customer.ID)
	if err != nil || len(stored.PasswordHash) == 0 || bytes.Contains(stored.PasswordHash, []byte("12345678")) {
		t.Fatal("customer password was not stored as a non-plaintext hash")
	}
	if err := auth.ValidatePasswordHash(stored.PasswordHash); err != nil {
		t.Fatal("stored customer hash is not a valid password hash")
	}
}

func TestBusinessVLESSSetupFailureReturnsSavedPendingRule(t *testing.T) {
	f := newBusinessFixture(t)
	// The entry network remains valid. The setup is expected to stay pending
	// because this isolated fixture has no bound VLESS runtime material; the
	// handler must not turn that resumable setup failure into a false create
	// failure.
	input := f.ruleRequest()
	input.Name = "VLESS setup pending"
	input.Targets = []forwarding.Target{{Host: "target.example.test", Port: 443}}
	input.IngressProtocol = forwarding.IngressVLESSReality
	input.Protocol = forwarding.ProtocolTCP
	input.EgressMode = forwarding.EgressDirect
	input.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	input.VLESSSOCKS5Host = "landing.example.test"
	input.VLESSSOCKS5Port = 1080
	input.VLESSSOCKS5Username = "landing-user"
	input.VLESSSOCKS5Password = "landing-secret"
	input.VLESSFlow = "xtls-rprx-vision"
	input.RealityServerName = "www.example.com"
	input.RealityPublicKey = "AbCdEf0123456789AbCdEf0123456789AbCdEf01234"
	input.RealityShortID = "0123456789abcdef"

	var saved forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "vless-setup-pending", input, http.StatusCreated), "rule", &saved)
	if saved.ID == "" || saved.Status != forwarding.StatusPendingActivation || saved.ActivationReason != forwarding.ActivationReasonVLESSRuntimeMaterialPending {
		t.Fatalf("saved VLESS rule should remain pending after setup failure: %+v", saved)
	}
	var listed struct {
		Items []forwarding.Rule `json:"items"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules", "", nil, http.StatusOK), &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != saved.ID {
		t.Fatalf("setup failure must leave the saved rule visible: %+v", listed.Items)
	}
}

func TestBusinessVLESSRealityVisionRuleLifecycleAndShapeGuards(t *testing.T) {
	f := newBusinessFixture(t)
	input := f.ruleRequest()
	input.Name = "VLESS Reality 直出"
	input.Targets = []forwarding.Target{{Host: "target.example.test", Port: 443}}
	input.IngressProtocol = forwarding.IngressVLESSReality
	input.Protocol = forwarding.ProtocolTCP
	input.EgressMode = forwarding.EgressDirect
	input.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	input.VLESSSOCKS5Host = "landing.example.test"
	input.VLESSSOCKS5Port = 1080
	input.VLESSSOCKS5Username = "landing-user"
	input.VLESSSOCKS5Password = "landing-secret"
	input.VLESSFlow = "xtls-rprx-vision"
	input.RealityServerName = "www.example.com"
	input.RealityPublicKey = "AbCdEf0123456789AbCdEf0123456789AbCdEf01234"
	input.RealityShortID = "0123456789abcdef"
	var created forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "vless-reality-create", input, http.StatusCreated), "rule", &created)
	if created.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || created.IngressStatus != forwarding.IngressReady || created.VLESSFlow != input.VLESSFlow || created.RealityServerName != input.RealityServerName || created.RealityPublicKey != input.RealityPublicKey || created.RealityShortID != input.RealityShortID {
		t.Fatalf("VLESS Reality rule was not persisted canonically: %+v", created)
	}
	if created.EgressMode != forwarding.EgressDirect || created.Protocol != forwarding.ProtocolTCP || created.Deployed {
		t.Fatalf("unexpected VLESS lifecycle state: %+v", created)
	}

	var exported forwarding.ExportDocument
	decodeResponse(t, businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules/export", "", nil, http.StatusOK), &exported)
	if len(exported.Rules) == 0 {
		t.Fatal("VLESS rule was missing from export")
	}
	found := false
	for _, item := range exported.Rules {
		if item.ID == created.ID {
			found = true
			if item.Rule.IngressProtocol != forwarding.IngressVLESSReality || item.Rule.RealityServerName != input.RealityServerName || item.Rule.RealityPublicKey != input.RealityPublicKey || item.Rule.RealityShortID != input.RealityShortID {
				t.Fatalf("export dropped VLESS Reality fields: %+v", item.Rule)
			}
		}
	}
	if !found {
		t.Fatalf("export did not contain created rule %s", created.ID)
	}

	invalidExit := input
	invalidExit.Name = "VLESS Reality 出口路径"
	invalidExit.EgressMode = forwarding.EgressExitGroup
	invalidExit.ExitGroupID = f.exit.ID
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "vless-reality-exit", invalidExit, http.StatusBadRequest)
	invalidUDP := input
	invalidUDP.Name = "VLESS Reality UDP"
	invalidUDP.Protocol = forwarding.ProtocolUDP
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "vless-reality-udp", invalidUDP, http.StatusBadRequest)
}

func TestForwardingTransferAPIRequiresAdminAndCommitsOnlyValidPreview(t *testing.T) {
	f := newBusinessFixture(t)
	input := forwarding.TransferRequest{Format: forwarding.ImportFormatNYText, Content: "导入规则##target.example.test#443", Mapping: forwarding.ImportMapping{
		CustomerID: f.customer.ID, EntryGroupID: f.entry.ID, EgressMode: forwarding.EgressDirect,
		Protocol: forwarding.ProtocolTCP, SelectionPolicy: forwarding.SelectionRoundRobin,
	}}
	for _, path := range []string{"/api/v1/forwarding-rules/import/preview", "/api/v1/forwarding-rules/import"} {
		requestJSON(t, f.handler, http.MethodPost, path, "", input, http.StatusUnauthorized)
	}
	requestJSON(t, f.handler, http.MethodGet, "/api/v1/forwarding-rules/export", "", nil, http.StatusUnauthorized)
	var preview struct {
		Preview forwarding.ImportPreview `json:"preview"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/import/preview", "", input, http.StatusOK), &preview)
	if !preview.Preview.CanCommit() || preview.Preview.Rows[0].ListenPort != 20000 {
		t.Fatalf("unexpected API preview: %+v", preview.Preview)
	}
	var imported struct {
		Result   forwarding.ImportResult `json:"result"`
		Replayed bool                    `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/import", "api-import", input, http.StatusCreated), &imported)
	if imported.Replayed || imported.Result.Created != 1 || len(imported.Result.Items) != 1 {
		t.Fatalf("unexpected import: %+v", imported)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/forwarding-rules/export", nil)
	request.Header.Set("Authorization", "Bearer "+f.token)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Disposition"), "forwarding-rules-") {
		t.Fatalf("export response missing attachment metadata: %d %s", recorder.Code, recorder.Header().Get("Content-Disposition"))
	}
	var document forwarding.ExportDocument
	decodeResponse(t, recorder.Body.Bytes(), &document)
	if document.SchemaVersion != forwarding.ExportSchemaV1 || len(document.Rules) != 1 || document.Rules[0].Rule.Targets[0].Host != "target.example.test" {
		t.Fatalf("unexpected export document: %+v", document)
	}
}

func TestBusinessRoutesRequireAdministratorAndValidMutations(t *testing.T) {
	f := newBusinessFixture(t)
	for _, path := range []string{"/api/v1/customers", "/api/v1/user-groups", "/api/v1/forwarding-rules", "/api/v1/rule-groups", "/api/v1/group-networks", "/api/v1/group-networks/" + f.entry.ID} {
		requestJSON(t, f.handler, http.MethodGet, path, "", nil, http.StatusUnauthorized)
	}
	for _, path := range []string{"/api/v1/customers", "/api/v1/user-groups", "/api/v1/forwarding-rules", "/api/v1/rule-groups", "/api/v1/forwarding-rules/batch"} {
		requestJSON(t, f.handler, http.MethodPost, path, "", map[string]any{}, http.StatusUnauthorized)
	}
	requestJSON(t, f.handler, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "", map[string]any{}, http.StatusUnauthorized)
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "", f.ruleRequest(), http.StatusBadRequest)
	unsafe := f.ruleRequest()
	unsafe.Targets = []forwarding.Target{{Host: "socks5://person:secret@127.0.0.1", Port: 9000}}
	response := businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "unsafe-forwarding", unsafe, http.StatusBadRequest)
	if strings.Contains(string(response), "person:secret") {
		t.Fatal("validation response echoed credentials")
	}
	for _, path := range []string{"/api/v1/customers", "/api/v1/user-groups", "/api/v1/group-networks"} {
		response := businessRequest(t, f, http.MethodGet, path, "", nil, http.StatusOK)
		if bytes.Contains(response, []byte("password")) {
			t.Fatalf("%s exposed password fields", path)
		}
	}
}

func TestRuleGroupAndBatchForwardingAPI(t *testing.T) {
	f := newBusinessFixture(t)
	var group rulegroups.RuleGroup
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/rule-groups", "rule-group-create", rulegroups.Request{Name: "核心规则", Description: "日常线路"}, http.StatusCreated), "rule_group", &group)
	if group.ID == "" || group.Revision != 1 {
		t.Fatalf("unexpected rule group: %+v", group)
	}
	var groupReplay struct {
		RuleGroup rulegroups.RuleGroup `json:"rule_group"`
		Replayed  bool                 `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/rule-groups", "rule-group-create", rulegroups.Request{Name: "核心规则", Description: "日常线路"}, http.StatusOK), &groupReplay)
	if !groupReplay.Replayed || groupReplay.RuleGroup.ID != group.ID {
		t.Fatal("rule-group replay did not return the original group")
	}

	input := f.ruleRequest()
	input.RuleGroupID = group.ID
	var first forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "batch-rule-one", input, http.StatusCreated), "rule", &first)
	input.Name = "客户备用直连"
	var second forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "batch-rule-two", input, http.StatusCreated), "rule", &second)

	pause := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{first.ID, second.ID}, ExpectedRevisions: map[string]int64{first.ID: 1, second.ID: 1}}
	var paused struct {
		Result   rulegroups.BatchResult `json:"result"`
		Replayed bool                   `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", "batch-pause", pause, http.StatusOK), &paused)
	if paused.Replayed || len(paused.Result.Items) != 2 || !paused.Result.Items[0].Paused || !paused.Result.Items[1].Paused {
		t.Fatalf("unexpected batch pause: %+v", paused)
	}
	auditCount := len(f.store.AuditEvents())
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", "batch-pause", pause, http.StatusOK), &paused)
	if !paused.Replayed || len(f.store.AuditEvents()) != auditCount {
		t.Fatal("batch replay duplicated its audit event")
	}

	staleMove := rulegroups.BatchRequest{Operation: rulegroups.BatchMoveGroup, RuleGroupID: "", RuleIDs: []string{first.ID, second.ID}, ExpectedRevisions: map[string]int64{first.ID: 2, second.ID: 1}}
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", "batch-stale", staleMove, http.StatusConflict)
	var listed struct {
		Items []forwarding.Rule `json:"items"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules", "", nil, http.StatusOK), &listed)
	for _, item := range listed.Items {
		if item.RuleGroupID != group.ID || item.Revision != 2 || !item.Paused {
			t.Fatalf("failed batch partially wrote rule: %+v", item)
		}
	}

	update := rulegroups.Request{Name: "核心规则已更新", Description: group.Description, Revision: group.Revision}
	var updated rulegroups.RuleGroup
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/rule-groups/"+group.ID, "rule-group-update", update, http.StatusOK), "rule_group", &updated)
	if updated.Revision != 2 || updated.Name != update.Name {
		t.Fatalf("rule group update failed: %+v", updated)
	}
	businessRequest(t, f, http.MethodPut, "/api/v1/rule-groups/"+group.ID, "rule-group-stale", update, http.StatusConflict)
}

func TestBusinessAuthorizationPortReservationAndNetworkCAS(t *testing.T) {
	f := newBusinessFixture(t)
	input := f.ruleRequest()
	input.ListenPort = 20001
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "reserve-port-1", input, http.StatusCreated)
	input.Name = "占用端口"
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "reserve-port-2", input, http.StatusConflict)
	input.ListenPort = 20010
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "outside-range", input, http.StatusBadRequest)
	other := createGroup(t, f.handler, f.token, "未授权入口", groups.KindEntry)
	input.ListenPort = 0
	input.EntryGroupID = other.ID
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "unauthorized-entry", input, http.StatusBadRequest)
	input = f.ruleRequest()
	input.EgressMode = forwarding.EgressExitGroup
	input.ExitGroupID = f.exit.ID
	exitNetwork := groupconfig.Request{DirectPolicy: groupconfig.DirectPolicyDisabled, TrafficMultiplier: 1, AllowedEntryGroupIDs: []string{f.entry.ID}}
	businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.exit.ID, "authorized-exit-network", exitNetwork, http.StatusOK)
	var issued enrollment.IssueResult
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token, map[string]any{"name": "exit-fixture", "expires_in_seconds": 900}, http.StatusCreated), &issued)
	var enrolled agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{
		EnrollmentToken: issued.Token, Hostname: "exit.example.test", Platform: "linux", Architecture: "amd64", AgentVersion: "0.1.0", Capabilities: []string{"gost"},
	}, http.StatusCreated), &enrolled)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/device-groups/"+f.exit.ID+"/members", f.token, map[string]any{
		"node_id": enrolled.NodeID, "dial_host": "exit.example.test", "weight": 100, "priority": 0,
	}, http.StatusOK)
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "authorized-exit-route", input, http.StatusCreated)
	input.ExitGroupID = other.ID
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "invalid-exit-route", input, http.StatusBadRequest)
	networkInput := groupconfig.Request{ConnectHost: "entry.example.test", PortStart: 20000, PortEnd: 20009, AllowDirect: true, AllowedExitGroupIDs: []string{f.exit.ID}, TrafficMultiplier: 2, Revision: 1}
	var network groupconfig.GroupNetwork
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "network-multiplier", networkInput, http.StatusOK), "network", &network)
	if network.Revision != 2 || network.TrafficMultiplier != 2 {
		t.Fatal("network replacement was not saved")
	}
	businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "network-multiplier", networkInput, http.StatusOK)
	businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "network-stale-cas", networkInput, http.StatusConflict)
	networkInput.Revision = 2
	networkInput.PortStart = 20002
	businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "network-invalidate-port", networkInput, http.StatusConflict)
	networkInput.PortStart = 20000
	networkInput.AllowDirect = false
	businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "network-invalidate-direct", networkInput, http.StatusConflict)
}

func TestGroupNetworkDirectPolicyAPIAndExitShape(t *testing.T) {
	f := newBusinessFixture(t)

	hiddenExitFields := groupconfig.Request{
		ConnectHost:       "hidden-entry.example.test",
		PortStart:         21000,
		PortEnd:           21010,
		DirectPolicy:      groupconfig.DirectPolicyDisabled,
		TrafficMultiplier: 1,
	}
	businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.exit.ID, "exit-hidden-fields", hiddenExitFields, http.StatusBadRequest)

	var exitNetwork groupconfig.GroupNetwork
	validExit := groupconfig.Request{DirectPolicy: groupconfig.DirectPolicyDisabled, TrafficMultiplier: 1}
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.exit.ID, "exit-network", validExit, http.StatusOK), "network", &exitNetwork)
	if exitNetwork.DirectPolicy != groupconfig.DirectPolicyDisabled || exitNetwork.AllowDirect || exitNetwork.ConnectHost != "" || exitNetwork.PortStart != 0 || exitNetwork.PortEnd != 0 {
		t.Fatalf("EXIT network exposed entry fields: %+v", exitNetwork)
	}

	forcedInput := groupconfig.Request{
		ConnectHost:       "entry.example.test",
		PortStart:         20000,
		PortEnd:           20009,
		DirectPolicy:      groupconfig.DirectPolicyForced,
		TrafficMultiplier: 1,
		Revision:          1,
	}
	var forced groupconfig.GroupNetwork
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/group-networks/"+f.entry.ID, "entry-forced", forcedInput, http.StatusOK), "network", &forced)
	if forced.DirectPolicy != groupconfig.DirectPolicyForced || !forced.AllowDirect || len(forced.AllowedExitGroupIDs) != 0 {
		t.Fatalf("FORCED policy was not returned canonically: %+v", forced)
	}

	direct := f.ruleRequest()
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "forced-direct-rule", direct, http.StatusCreated)
	exit := f.ruleRequest()
	exit.EgressMode = forwarding.EgressExitGroup
	exit.ExitGroupID = f.exit.ID
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "forced-exit-rule", exit, http.StatusBadRequest)
}

func TestBusinessCustomerDisableAndExpiryPreventResume(t *testing.T) {
	f := newBusinessFixture(t)
	originalCustomer, err := f.store.Customer(context.Background(), f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	ruleInput := f.ruleRequest()
	forwardingService := forwarding.NewService(f.store, nil)
	rule, _, err := forwardingService.CreateForCustomer(context.Background(), f.customer.ID, ruleInput, "availability-rule")
	if err != nil {
		t.Fatal(err)
	}
	customerInput := customers.CustomerInput{Username: f.customer.Username, DisplayName: f.customer.DisplayName, UserGroupID: f.userGroup.ID, Disabled: true, Revision: 1, MaxRules: 3, TrafficLimitBytes: 1024}
	var customer customers.Customer
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/customers/"+f.customer.ID, "disable-customer", customerInput, http.StatusOK), "customer", &customer)
	if customer.Status != customers.StatusDisabled || customer.Revision != 2 {
		t.Fatal("customer was not disabled")
	}
	listed, err := forwardingService.GetForCustomer(context.Background(), f.customer.ID, rule.ID)
	if err != nil || listed.Status != forwarding.StatusCustomerDisabled || listed.Deployed {
		t.Fatal("existing rule did not reflect disabled customer")
	}
	if _, _, err := forwardingService.CreateForCustomer(context.Background(), f.customer.ID, ruleInput, "disabled-new-rule"); err == nil {
		t.Fatal("disabled customer created a rule")
	}
	ruleInput.Revision = rule.Revision
	ruleInput.Paused = true
	rule, _, err = forwardingService.UpdateForCustomer(context.Background(), f.customer.ID, rule.ID, ruleInput, "pause-disabled-rule")
	if err != nil {
		t.Fatal(err)
	}
	ruleInput.Revision = rule.Revision
	ruleInput.Paused = false
	if _, _, err := forwardingService.UpdateForCustomer(context.Background(), f.customer.ID, rule.ID, ruleInput, "resume-disabled-rule"); err == nil {
		t.Fatal("disabled customer resumed a rule")
	}
	expired := time.Now().UTC().Add(-time.Hour)
	customerInput.Disabled = false
	customerInput.ExpiresAt = &expired
	customerInput.Revision = 2
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/customers/"+f.customer.ID, "expire-customer", customerInput, http.StatusOK), "customer", &customer)
	if customer.Status != customers.StatusExpired {
		t.Fatal("expired customer did not reflect actual availability")
	}
	if _, _, err := forwardingService.UpdateForCustomer(context.Background(), f.customer.ID, rule.ID, ruleInput, "resume-expired-rule"); err == nil {
		t.Fatal("expired customer resumed a rule")
	}
	stored, err := f.store.Customer(context.Background(), f.customer.ID)
	if err != nil || !bytes.Equal(stored.PasswordHash, originalCustomer.PasswordHash) {
		t.Fatal("blank password on customer edit reset the password")
	}
}

func TestBusinessRuleQuotaAndAuthorizationReplacementAreAtomic(t *testing.T) {
	f := newBusinessFixture(t)
	forwardingService := forwarding.NewService(f.store, nil)
	for index, key := range []string{"quota-first-rule", "quota-second-rule", "quota-third-rule"} {
		input := f.ruleRequest()
		input.Paused = index == 2
		if _, _, err := forwardingService.CreateForCustomer(context.Background(), f.customer.ID, input, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := forwardingService.CreateForCustomer(context.Background(), f.customer.ID, f.ruleRequest(), "quota-fourth-rule"); err == nil {
		t.Fatal("fourth customer rule ignored quota")
	}
	groupInput := customers.UserGroupInput{Name: f.userGroup.Name, AllowedEntryGroupIDs: []string{f.entry.ID}, AllowedExitGroupIDs: []string{f.exit.ID}, AllowDirect: true, Revision: 1}
	groupInput.Description = "已编辑用户组说明"
	var group customers.UserGroup
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, "/api/v1/user-groups/"+f.userGroup.ID, "group-description", groupInput, http.StatusOK), "user_group", &group)
	if group.Revision != 2 || group.Description != groupInput.Description {
		t.Fatal("group update did not persist")
	}
	businessRequest(t, f, http.MethodPut, "/api/v1/user-groups/"+f.userGroup.ID, "group-description", groupInput, http.StatusOK)
	businessRequest(t, f, http.MethodPut, "/api/v1/user-groups/"+f.userGroup.ID, "group-stale-update", groupInput, http.StatusConflict)
	groupInput.Revision = 2
	groupInput.AllowDirect = false
	groupInput.Description = "不可部分写入"
	auditsBefore := len(f.store.AuditEvents())
	businessRequest(t, f, http.MethodPut, "/api/v1/user-groups/"+f.userGroup.ID, "group-revoke-existing", groupInput, http.StatusConflict)
	stored, err := f.store.UserGroup(context.Background(), f.userGroup.ID)
	if err != nil || stored.Revision != 2 || !stored.AllowDirect || stored.Description != "已编辑用户组说明" || len(f.store.AuditEvents()) != auditsBefore {
		t.Fatal("failed authorization mutation partially changed group or audit")
	}
	customerInput := customers.CustomerInput{Username: f.customer.Username, DisplayName: f.customer.DisplayName, UserGroupID: f.userGroup.ID, MaxRules: 2, Revision: 1}
	businessRequest(t, f, http.MethodPut, "/api/v1/customers/"+f.customer.ID, "customer-lower-quota", customerInput, http.StatusConflict)
	customer, err := f.store.Customer(context.Background(), f.customer.ID)
	if err != nil || customer.Revision != 1 || customer.MaxRules != 3 {
		t.Fatal("failed customer quota mutation partially committed")
	}
}

func TestAdministratorForwardingRoutesOwnRulesOnly(t *testing.T) {
	f := newBusinessFixture(t)
	forwardingService := forwarding.NewService(f.store, nil)
	foreign, _, err := forwardingService.CreateForCustomer(context.Background(), f.customer.ID, f.ruleRequest(), "foreign-customer-rule")
	if err != nil {
		t.Fatal(err)
	}
	input := f.ruleRequest()
	var own forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "admin-owned-rule", input, http.StatusCreated), "rule", &own)
	if !own.OwnedByAdministrator("adm_business") || own.CustomerID == f.customer.ID {
		t.Fatalf("administrator request retained client-supplied customer ownership: %+v", own)
	}
	var listed struct {
		Items []forwarding.Rule `json:"items"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules", "", nil, http.StatusOK), &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != own.ID {
		t.Fatalf("administrator list leaked foreign rule: %+v", listed.Items)
	}
	businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules/"+foreign.ID, "", nil, http.StatusNotFound)
	input.Revision = foreign.Revision
	businessRequest(t, f, http.MethodPut, "/api/v1/forwarding-rules/"+foreign.ID, "foreign-update", input, http.StatusNotFound)
	batch := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{own.ID, foreign.ID}, ExpectedRevisions: map[string]int64{own.ID: own.Revision, foreign.ID: foreign.Revision}}
	auditCount := len(f.store.AuditEvents())
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", "foreign-batch", batch, http.StatusNotFound)
	unchanged, err := f.store.ForwardingRule(context.Background(), own.ID)
	if err != nil || unchanged.Paused || unchanged.Revision != own.Revision || len(f.store.AuditEvents()) != auditCount {
		t.Fatalf("mixed-owner batch partially changed own rule or audit: %+v %v", unchanged, err)
	}
	var exported forwarding.ExportDocument
	decodeResponse(t, businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules/export", "", nil, http.StatusOK), &exported)
	if len(exported.Rules) != 1 || exported.Rules[0].ID != own.ID {
		t.Fatalf("administrator export leaked foreign rule: %+v", exported.Rules)
	}
	if _, err := f.store.EncodeSnapshot(); err != nil {
		t.Fatalf("administrator rule cannot be snapshotted: %v", err)
	}
	stolen := f.ruleRequest()
	stolen.Revision = foreign.Revision
	stolen.Name = "attempted takeover"
	content, err := json.Marshal(forwarding.ExportDocument{SchemaVersion: forwarding.ExportSchemaV1, Rules: []forwarding.ExportRule{{Operation: forwarding.ImportUpsert, ID: foreign.ID, Rule: stolen}}})
	if err != nil {
		t.Fatal(err)
	}
	transfer := forwarding.TransferRequest{Format: forwarding.ImportFormatJSONV1, Content: string(content)}
	var preview struct {
		Preview forwarding.ImportPreview `json:"preview"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/import/preview", "", transfer, http.StatusOK), &preview)
	if preview.Preview.Invalid != 1 || len(preview.Preview.Rows) != 1 || preview.Preview.Rows[0].Issues[0].Code != "missing_resource" {
		t.Fatalf("cross-owner import preview accepted foreign rule: %+v", preview.Preview)
	}
	auditCount = len(f.store.AuditEvents())
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/import", "foreign-import", transfer, http.StatusConflict)
	unchanged, err = f.store.ForwardingRule(context.Background(), foreign.ID)
	if err != nil || unchanged.Name != foreign.Name || unchanged.Revision != foreign.Revision || len(f.store.AuditEvents()) != auditCount {
		t.Fatalf("cross-owner import changed rule or audit: %+v %v", unchanged, err)
	}
}

func TestAdministratorForwardingRoutesIsolateDifferentAdministrators(t *testing.T) {
	f := newBusinessFixture(t)
	second := businessFixtureWithSecondAdministrator(t, f)
	f.handler, f.store = second.handler, second.store
	var firstRule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "first-admin-rule", f.ruleRequest(), http.StatusCreated), "rule", &firstRule)
	var secondRule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, second, http.MethodPost, "/api/v1/forwarding-rules", "second-admin-rule", second.ruleRequest(), http.StatusCreated), "rule", &secondRule)
	if !firstRule.OwnedByAdministrator("adm_business") || !secondRule.OwnedByAdministrator("adm_second") {
		t.Fatalf("rules were not bound to their authenticated administrators: %+v %+v", firstRule, secondRule)
	}
	for _, tc := range []struct {
		fixture businessFixture
		own     forwarding.Rule
		other   forwarding.Rule
	}{
		{f, firstRule, secondRule},
		{second, secondRule, firstRule},
	} {
		var listed struct {
			Items []forwarding.Rule `json:"items"`
		}
		decodeResponse(t, businessRequest(t, tc.fixture, http.MethodGet, "/api/v1/forwarding-rules", "", nil, http.StatusOK), &listed)
		if len(listed.Items) != 1 || listed.Items[0].ID != tc.own.ID {
			t.Fatalf("administrator list crossed owner boundary: %+v", listed.Items)
		}
		businessRequest(t, tc.fixture, http.MethodGet, "/api/v1/forwarding-rules/"+tc.other.ID, "", nil, http.StatusNotFound)
		request := tc.fixture.ruleRequest()
		request.Revision = tc.other.Revision
		businessRequest(t, tc.fixture, http.MethodPut, "/api/v1/forwarding-rules/"+tc.other.ID, "cross-admin-update-"+tc.own.ID, request, http.StatusNotFound)
		batch := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{tc.other.ID}, ExpectedRevisions: map[string]int64{tc.other.ID: tc.other.Revision}}
		businessRequest(t, tc.fixture, http.MethodPost, "/api/v1/forwarding-rules/batch", "cross-admin-batch-"+tc.own.ID, batch, http.StatusNotFound)
		var exported forwarding.ExportDocument
		decodeResponse(t, businessRequest(t, tc.fixture, http.MethodGet, "/api/v1/forwarding-rules/export", "", nil, http.StatusOK), &exported)
		if len(exported.Rules) != 1 || exported.Rules[0].ID != tc.own.ID {
			t.Fatalf("administrator export crossed owner boundary: %+v", exported.Rules)
		}
	}
}

func TestAdministratorRuleGroupRoutesIsolateDifferentAdministrators(t *testing.T) {
	f := newBusinessFixture(t)
	second := businessFixtureWithSecondAdministrator(t, f)
	f.handler, f.store = second.handler, second.store
	var firstGroup, secondGroup rulegroups.RuleGroup
	request := rulegroups.Request{Name: "专用线路"}
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/rule-groups", "first-group", request, http.StatusCreated), "rule_group", &firstGroup)
	decodeBusinessEntity(t, businessRequest(t, second, http.MethodPost, "/api/v1/rule-groups", "second-group", request, http.StatusCreated), "rule_group", &secondGroup)
	if firstGroup.OwnerAdministratorID != "adm_business" || secondGroup.OwnerAdministratorID != "adm_second" {
		t.Fatalf("rule groups did not retain their owners: %+v %+v", firstGroup, secondGroup)
	}
	for _, tc := range []struct {
		fixture businessFixture
		own     rulegroups.RuleGroup
		other   rulegroups.RuleGroup
	}{
		{f, firstGroup, secondGroup},
		{second, secondGroup, firstGroup},
	} {
		var listed struct {
			Items []rulegroups.RuleGroup `json:"items"`
		}
		decodeResponse(t, businessRequest(t, tc.fixture, http.MethodGet, "/api/v1/rule-groups", "", nil, http.StatusOK), &listed)
		if len(listed.Items) != 1 || listed.Items[0].ID != tc.own.ID {
			t.Fatalf("rule-group list crossed owner boundary: %+v", listed.Items)
		}
		businessRequest(t, tc.fixture, http.MethodGet, "/api/v1/rule-groups/"+tc.other.ID, "", nil, http.StatusNotFound)
		businessRequest(t, tc.fixture, http.MethodPut, "/api/v1/rule-groups/"+tc.other.ID, "foreign-group-update-"+tc.own.ID, rulegroups.Request{Name: "越权修改", Revision: tc.other.Revision}, http.StatusNotFound)
	}
	input := f.ruleRequest()
	input.RuleGroupID = firstGroup.ID
	var ownRule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "first-group-rule", input, http.StatusCreated), "rule", &ownRule)
	move := rulegroups.BatchRequest{Operation: rulegroups.BatchMoveGroup, RuleGroupID: secondGroup.ID, RuleIDs: []string{ownRule.ID}, ExpectedRevisions: map[string]int64{ownRule.ID: ownRule.Revision}}
	beforeAudits := len(f.store.AuditEvents())
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", "foreign-group-move", move, http.StatusNotFound)
	unchanged, err := f.store.ForwardingRule(context.Background(), ownRule.ID)
	if err != nil || unchanged.RuleGroupID != firstGroup.ID || unchanged.Revision != ownRule.Revision || len(f.store.AuditEvents()) != beforeAudits {
		t.Fatalf("cross-owner move changed rule or audit: %+v err=%v", unchanged, err)
	}
}

func businessFixtureWithSecondAdministrator(t *testing.T, original businessFixture) businessFixture {
	t.Helper()
	raw, err := original.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	var administrators map[string]json.RawMessage
	if err := json.Unmarshal(state["Administrators"], &administrators); err != nil {
		t.Fatal(err)
	}
	passwordHash, err := auth.HashPassword("isolated-second-administrator")
	if err != nil {
		t.Fatal(err)
	}
	administrators["second"] = mustBusinessJSON(t, struct {
		ID           string
		Username     string
		PasswordHash []byte
		CreatedAt    time.Time
	}{ID: "adm_second", Username: "second", PasswordHash: passwordHash, CreatedAt: time.Now().UTC()})
	state["Administrators"] = mustBusinessJSON(t, administrators)
	restored, err := memoryrepo.DecodeSnapshot(mustBusinessJSON(t, state))
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return time.Now().UTC() }
	handler := httpapi.New(auth.NewService(restored, audit.NewService(restored), clock, time.Hour), enrollment.NewService(restored, clock, time.Hour), nodes.NewService(restored, clock, time.Minute), groups.NewService(restored, clock), endpoints.NewService(restored, clock), generations.NewService(restored, clock), slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.WithBusiness(customers.NewService(restored, clock), forwarding.NewService(restored, clock), groupconfig.NewService(restored, clock)), httpapi.WithRuleGroups(rulegroups.NewService(restored, clock)))
	var login auth.LoginResult
	decodeResponse(t, requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "second", "password": "isolated-second-administrator"}, http.StatusOK), &login)
	return businessFixture{handler: handler, token: login.AccessToken, store: restored, entry: original.entry, exit: original.exit, userGroup: original.userGroup, customer: original.customer}
}

func mustBusinessJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
