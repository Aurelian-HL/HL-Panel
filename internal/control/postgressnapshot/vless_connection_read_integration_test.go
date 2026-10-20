package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

// Exercise the production HTTP injection with a real isolated database. No
// mocked repository may authorize a stale cached connection after a DB failure.
func TestPostgreSQLConnectionReadSourceTracksSnapshotHealthAndFailure(t *testing.T) {
	writer, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	// Publish a valid short lease and let it expire naturally. A newer proof
	// cannot shorten a persisted lease (CanReplace deliberately rejects that).
	proof.LeaseExpiresAt = time.Now().UTC().Add(5 * time.Second)
	activated, err := writer.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof})
	if err != nil || len(activated) != 1 || activated[0] != nil {
		t.Fatal("fixture activation", activated, err)
	}
	reader, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	identity, err := vlessidentity.NewService(reader, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := identity.CredentialForAdministrator(ctx, "admin-one", proof.RuleID)
	if err != nil {
		t.Fatal(err)
	}
	rules := forwarding.NewService(reader, time.Now)
	endpointService := endpoints.NewServiceWithReadyCandidateSource(reader, time.Now, gatewaymembership.NewService(reader))
	ordinary := vlessconnection.NewService(identity, rules, endpointService)
	want, err := ordinary.ResolveForwardingConnection(ctx, "admin-one", proof.RuleID)
	if err != nil || want.URI == "" {
		t.Fatal("ordinary connection not ready", err)
	}
	resolver := vlessconnection.NewService(identity, rules, endpointService, reader)
	authService := auth.NewService(reader, audit.NewService(reader), time.Now, time.Hour)
	login, err := authService.Login(ctx, "admin", "isolated-test-password")
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(authService, enrollment.NewService(reader, time.Now, time.Hour),
		nodes.NewService(reader, time.Now, time.Minute), groups.NewService(reader, time.Now), endpointService,
		generations.NewService(reader, time.Now), slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpapi.WithBusiness(customers.NewService(reader, time.Now), rules, groupconfig.NewService(reader, time.Now)),
		httpapi.WithVLESS(identity, nil), httpapi.WithVLESSConnections(resolver))
	paths := []string{"/api/v1/forwarding-rules/" + proof.RuleID + "/connection",
		"/api/v1/vless/identities/" + record.Binding.ID + "/connection"}
	checkHTTP := func(status int) {
		t.Helper()
		for _, path := range paths {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer "+login.AccessToken)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != status {
				t.Fatalf("connection route status=%d; expected=%d", response.Code, status)
			}
			var connection vlessconnection.Connection
			if err := json.Unmarshal(response.Body.Bytes(), &connection); err != nil {
				t.Fatal("invalid connection response", err)
			}
			if status == http.StatusOK && connection != want {
				t.Fatal("HTTP connection differs from ordinary domain service")
			}
			if status != http.StatusOK && connection.URI != "" {
				t.Fatal("unready HTTP response leaked a URI")
			}
		}
	}
	check := func(ready bool) {
		t.Helper()
		calls := []func() (vlessconnection.Connection, error){
			func() (vlessconnection.Connection, error) {
				return resolver.ResolveForwardingConnection(ctx, "admin-one", proof.RuleID)
			},
			func() (vlessconnection.Connection, error) {
				return resolver.ResolveIdentityConnection(ctx, "admin-one", record.Binding.ID)
			},
		}
		for _, call := range calls {
			got, err := call()
			if ready && (err != nil || got != want) {
				t.Fatal("source differs from ordinary domain service", err)
			}
			if !ready && (err == nil || got.URI != "") {
				t.Fatal("source issued an unavailable connection")
			}
		}
		lines, rejected, err := resolver.ResolveSubscriptionLines(ctx, "admin-one", record.Binding.CustomerID, []string{record.Binding.ID})
		if err != nil {
			t.Fatal(err)
		}
		if ready && (lines[record.Binding.ID] != want.URI || len(rejected) != 0) {
			t.Fatal("batch differs from connection routes")
		}
		if !ready && (len(lines) != 0 || rejected[record.Binding.ID] == nil) {
			t.Fatal("batch published an unavailable connection")
		}
	}
	check(true)
	checkHTTP(http.StatusOK)
	cached := reader.readCache.forwarding
	check(true)
	checkHTTP(http.StatusOK)
	if cached == nil || reader.readCache.forwarding != cached {
		t.Fatal("unchanged connection read rebuilt snapshot projection")
	}
	for _, call := range []func() (vlessconnection.Connection, error){
		func() (vlessconnection.Connection, error) {
			return resolver.ResolveForwardingConnection(ctx, "other", proof.RuleID)
		},
		func() (vlessconnection.Connection, error) {
			return resolver.ResolveIdentityConnection(ctx, "other", record.Binding.ID)
		},
	} {
		if got, err := call(); !errors.Is(err, faults.ErrNotFound) || got.URI != "" {
			t.Fatal("foreign administrator gained connection access")
		}
	}

	// A repair that preserves the application revision still changes xmin.
	original, _ := healthSnapshot(t, writer)
	var snapshot map[string]json.RawMessage
	var entries map[string]forwarding.Rule
	if err := json.Unmarshal(original, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(snapshot["ForwardRules"], &entries); err != nil {
		t.Fatal(err)
	}
	paused := entries[proof.RuleID]
	paused.Paused = true
	entries[proof.RuleID] = paused
	snapshot["ForwardRules"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(payload []byte) {
		t.Helper()
		result, err := writer.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1 WHERE singleton=true`, payload)
		if err != nil {
			t.Fatal("isolated snapshot replacement", err)
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			t.Fatal("isolated snapshot replacement did not affect the singleton row", count, err)
		}
	}
	replace(repaired)
	check(false)
	checkHTTP(http.StatusConflict)
	replace(original)
	check(true)

	// The unchanged cached projection must stop serving a URI at lease expiry.
	beforeExpiry := reader.readCache.forwarding
	time.Sleep(time.Until(proof.LeaseExpiresAt.Add(10 * time.Millisecond)))
	check(false)
	checkHTTP(http.StatusConflict)
	if reader.readCache.forwarding != beforeExpiry {
		t.Fatal("lease expiry unexpectedly rebuilt the snapshot projection")
	}
	renewed := proof
	renewed.VerifiedAt = time.Now().UTC()
	renewed.LeaseExpiresAt = renewed.VerifiedAt.Add(endpoints.DefaultHealthTTL)
	beforeRenewal, renewalRevision := healthSnapshot(t, writer)
	results, err := writer.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{renewed})
	if err != nil || len(results) != 1 || results[0] != nil {
		t.Fatal("health renewal", results, err)
	}
	afterRenewal, nextRevision := healthSnapshot(t, writer)
	if !bytes.Equal(beforeRenewal, afterRenewal) || nextRevision != renewalRevision+1 {
		t.Fatal("health renewal rewrote snapshot or lost revision")
	}
	check(true)
	checkHTTP(http.StatusOK)
	// Another Store observes the new revision and must revalidate it. Once
	// validated, later reads reuse that projection, with fresh live overlays.
	afterRenewalCache := reader.readCache.forwarding
	check(true)
	checkHTTP(http.StatusOK)
	if afterRenewalCache == nil || reader.readCache.forwarding != afterRenewalCache {
		t.Fatal("validated health renewal was not reused by subsequent reads")
	}

	replace([]byte(`{"Version":24,"invalid":true}`))
	for _, call := range []func() (vlessconnection.Connection, error){
		func() (vlessconnection.Connection, error) {
			return resolver.ResolveForwardingConnection(ctx, "admin-one", proof.RuleID)
		},
		func() (vlessconnection.Connection, error) {
			return resolver.ResolveIdentityConnection(ctx, "admin-one", record.Binding.ID)
		},
	} {
		if got, err := call(); err == nil || got.URI != "" {
			t.Fatal("corrupt database served a cached connection")
		}
	}
	if lines, _, err := resolver.ResolveSubscriptionLines(ctx, "admin-one", record.Binding.CustomerID, []string{record.Binding.ID}); err == nil || len(lines) != 0 {
		t.Fatal("corrupt database served cached subscription lines")
	}
	replace(original)
	check(true)
	writerIdentity, err := vlessidentity.NewService(writer, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := writerIdentity.Revoke(ctx, "admin-one", record.Binding.ID, vlessidentity.MutationRequest{Revision: record.Binding.Revision}, "isolated-revoke"); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.ResolveForwardingConnection(ctx, "admin-one", proof.RuleID); err == nil || got.URI != "" {
		t.Fatal("unavailable database served a cached connection")
	}
	if got, err := resolver.ResolveIdentityConnection(ctx, "admin-one", record.Binding.ID); err == nil || got.URI != "" {
		t.Fatal("unavailable database served a cached identity")
	}
	if lines, _, err := resolver.ResolveSubscriptionLines(ctx, "admin-one", record.Binding.CustomerID, []string{record.Binding.ID}); err == nil || len(lines) != 0 {
		t.Fatal("unavailable database served cached subscription lines")
	}
}
