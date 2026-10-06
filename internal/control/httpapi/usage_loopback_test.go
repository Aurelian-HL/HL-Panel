package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/agent/controlclient"
	agentusage "github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	controlusage "github.com/hongle/hl-panel/internal/control/usage"
	usagememory "github.com/hongle/hl-panel/internal/control/usage/memory"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type loopbackPolicy struct{ limit int64 }

func (policy loopbackPolicy) UsagePolicy(_ context.Context, customerID string) (controlusage.CustomerPolicy, error) {
	return controlusage.CustomerPolicy{CustomerID: customerID, TrafficLimitBytes: policy.limit}, nil
}

type loopbackCounters struct {
	calls  int
	deltas []agentusage.CounterDelta
}

func (source *loopbackCounters) CollectUsage(context.Context, agentusage.CollectionWindow) ([]agentusage.CounterDelta, error) {
	source.calls++
	return append([]agentusage.CounterDelta(nil), source.deltas...), nil
}

// lostAcknowledgementSender simulates a server response disappearing after the
// server has committed the report. A restarted reporter must resend exactly the
// journaled payload and rely on server-side idempotency.
type lostAcknowledgementSender struct {
	delegate *controlclient.Client
	lost     bool
	report   agentv1.UsageReport
}

func (sender *lostAcknowledgementSender) ReportUsage(ctx context.Context, credential string, report agentv1.UsageReport) (agentv1.UsageAcknowledgement, error) {
	sender.report = report
	acknowledgement, err := sender.delegate.ReportUsage(ctx, credential, report)
	if err != nil {
		return agentv1.UsageAcknowledgement{}, err
	}
	if !sender.lost {
		sender.lost = true
		return agentv1.UsageAcknowledgement{}, errors.New("simulated acknowledgement loss")
	}
	return acknowledgement, nil
}

type loopbackExecutor struct{ commands []agentv1.EnforcementCommand }

func (executor *loopbackExecutor) ApplyEnforcement(_ context.Context, command agentv1.EnforcementCommand) error {
	executor.commands = append(executor.commands, command)
	return nil
}

func TestUsageLoopbackJournalAndEnforcementLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	passwordHash, err := auth.HashPassword("loopback-administrator-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm_loopback", Username: "admin", PasswordHash: passwordHash, CreatedAt: now})
	auditService := audit.NewService(store)
	authService := auth.NewService(store, auditService, clock, time.Hour)
	enrollmentService := enrollment.NewService(store, clock, time.Hour)
	nodeService := nodes.NewService(store, clock, time.Minute)
	usageService := controlusage.NewService(usagememory.New(), loopbackPolicy{limit: 100}, clock)
	handler := httpapi.New(
		authService, enrollmentService, nodeService, groups.NewService(store, clock), endpoints.NewService(store, clock),
		generations.NewService(store, clock), slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.WithUsage(usageService),
	)
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := controlclient.NewWithOptions(server.URL, server.Client(), true)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := enrollmentService.Issue(ctx, "adm_loopback", "usage-loopback", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := client.Enroll(ctx, agentv1.EnrollmentRequest{
		EnrollmentToken: issued.Token, Hostname: "loopback-node", Platform: "linux", Architecture: "amd64", AgentVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	login, err := authService.Login(ctx, "admin", "loopback-administrator-password")
	if err != nil {
		t.Fatal(err)
	}

	journalStore := agentusage.NewJournalStore(filepath.Join(t.TempDir(), "usage-journal.json"))
	source := &loopbackCounters{deltas: []agentusage.CounterDelta{{
		CustomerID: "customer-one", RuleID: "rule-one", EntryGroupID: "entry-one", Protocol: "tcp",
		RuleActualBytes: 100, CustomerActualBytes: 100,
		EntryMultiplierMicros: agentv1.UsageMultiplierScale, ExitMultiplierMicros: agentv1.UsageMultiplierScale,
	}}}
	lostSender := &lostAcknowledgementSender{delegate: client}
	reporter, err := agentusage.NewReporter(journalStore, source, lostSender, enrolled.NodeID, enrolled.NodeCredential, 10, clock)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := reporter.FlushOnce(ctx); err == nil || err.Error() != "simulated acknowledgement loss" {
		t.Fatalf("first flush error=%v", err)
	}
	pending, err := journalStore.Load()
	if err != nil || len(pending.Pending) != 1 || pending.Pending[0] != lostSender.report {
		t.Fatalf("pending journal=%#v error=%v", pending, err)
	}
	restartedReporter, err := agentusage.NewReporter(journalStore, source, client, enrolled.NodeID, enrolled.NodeCredential, 10, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := restartedReporter.FlushOnce(ctx); err != nil {
		t.Fatal(err)
	}
	journal, err := journalStore.Load()
	if err != nil || len(journal.Pending) != 0 || journal.NextSequence != 2 || source.calls != 1 {
		t.Fatalf("journal after replay=%#v source calls=%d error=%v", journal, source.calls, err)
	}

	result := queryUsageOverHTTP(t, server, login.AccessToken)
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Decision == nil || result.Items[0].Decision.Status != controlusage.EnforcementPending {
		t.Fatalf("pending usage query=%#v", result)
	}
	decisionID := result.Items[0].Decision.ID
	executor := &loopbackExecutor{}
	reconciler, err := agentusage.NewEnforcementReconciler(client, executor, enrolled.NodeCredential)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := reconciler.ReconcileOnce(ctx); err != nil || !applied {
		t.Fatalf("disable reconciliation applied=%v error=%v", applied, err)
	}
	result = queryUsageOverHTTP(t, server, login.AccessToken)
	if result.Items[0].Decision.Status != controlusage.EnforcementApplied {
		t.Fatalf("disable was not acknowledged as applied: %#v", result.Items[0].Decision)
	}

	revoke := postRevokeOverHTTP(t, server, login.AccessToken, decisionID)
	if revoke.Decision.Status != controlusage.EnforcementRevokePending || revoke.Decision.Action != agentv1.EnforcementEnableCustomer || revoke.Replayed {
		t.Fatalf("revoke response=%#v", revoke)
	}
	if applied, err := reconciler.ReconcileOnce(ctx); err != nil || !applied {
		t.Fatalf("enable reconciliation applied=%v error=%v", applied, err)
	}
	result = queryUsageOverHTTP(t, server, login.AccessToken)
	if result.Items[0].Decision.Status != controlusage.EnforcementRevoked {
		t.Fatalf("enable was not acknowledged as revoked: %#v", result.Items[0].Decision)
	}
	if len(executor.commands) != 2 || executor.commands[0].Action != agentv1.EnforcementDisableCustomer || executor.commands[1].Action != agentv1.EnforcementEnableCustomer {
		t.Fatalf("executor commands=%#v", executor.commands)
	}
}

func queryUsageOverHTTP(t *testing.T, server *httptest.Server, token string) controlusage.QueryResult {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/usage?scope=site&page=1&page_size=25", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("query status=%d body=%s", response.StatusCode, body)
	}
	var result controlusage.QueryResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func postRevokeOverHTTP(t *testing.T, server *httptest.Server, token, decisionID string) struct {
	Decision controlusage.EnforcementDecision `json:"decision"`
	Replayed bool                             `json:"replayed"`
} {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/usage/enforcement/"+decisionID+"/revoke", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "revoke-loopback-one")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("revoke status=%d body=%s", response.StatusCode, body)
	}
	var result struct {
		Decision controlusage.EnforcementDecision `json:"decision"`
		Replayed bool                             `json:"replayed"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
