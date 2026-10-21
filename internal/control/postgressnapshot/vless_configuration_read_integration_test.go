package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

func TestPostgreSQLConfigurationReadTracksAuthorityWithoutMutations(t *testing.T) {
	writer, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	reader, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	resolver := vlessconnection.NewService(nil, nil, nil, reader)
	original, revision := healthSnapshot(t, writer)
	read := func() vlessconnection.Configuration {
		t.Helper()
		before, rev := healthSnapshot(t, writer)
		got, err := resolver.ResolveForwardingConfiguration(ctx, "admin-one", proof.RuleID)
		if err != nil || got.URI == "" || got.ServiceReady || got.LiveReadinessChecked {
			t.Fatal("saved configuration read failed", err)
		}
		after, next := healthSnapshot(t, writer)
		if !bytes.Equal(before, after) || rev != next {
			t.Fatal("configuration read wrote the authoritative snapshot")
		}
		return got
	}
	configured := read()
	if configured.Status != "configured" || configured.Paused {
		t.Fatal("saved configured state was misreported")
	}
	cached := reader.readCache.forwarding
	if again := read(); again != configured || cached == nil || reader.readCache.forwarding != cached {
		t.Fatal("unchanged authoritative configuration changed")
	}
	replace := func(raw []byte) {
		t.Helper()
		result, err := writer.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1 WHERE singleton=true`, raw)
		if err != nil {
			t.Fatal("isolated snapshot replacement failed", err)
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			t.Fatal("isolated singleton replacement failed")
		}
	}
	editRule := func(paused bool, sni string) []byte {
		t.Helper()
		var snapshot map[string]json.RawMessage
		var rules map[string]forwarding.Rule
		if err := json.Unmarshal(original, &snapshot); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(snapshot["ForwardRules"], &rules); err != nil {
			t.Fatal(err)
		}
		r := rules[proof.RuleID]
		r.Paused = paused
		if sni != "" {
			r.RealityServerName, r.RealityDestination = sni, sni+":443"
		}
		rules[r.ID] = r
		snapshot["ForwardRules"], err = json.Marshal(rules)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// An external repair can preserve revision; xmin must invalidate the cache.
	replace(editRule(true, ""))
	paused := read()
	if paused.Status != "paused" || !paused.Paused || paused.RuleStatus != forwarding.StatusPaused || paused.URI != configured.URI || reader.readCache.forwarding == cached {
		t.Fatal("same-revision pause was not observed, or changed identity")
	}
	if _, rev := healthSnapshot(t, writer); rev != revision {
		t.Fatal("test repair unexpectedly changed application revision")
	}
	replace(editRule(true, "example.org"))
	changed := read()
	u, err := url.Parse(changed.URI)
	beforeURI, _ := url.Parse(configured.URI)
	if err != nil || u.User == nil || u.Query().Get("sni") != "example.org" || u.User.Username() != beforeURI.User.Username() || changed.Endpoint != configured.Endpoint {
		t.Fatal("saved material did not follow authority or rotated identity")
	}
	if got, err := resolver.ResolveForwardingConfiguration(ctx, "foreign-admin", proof.RuleID); !errors.Is(err, faults.ErrNotFound) || got != (vlessconnection.Configuration{}) {
		t.Fatal("foreign administrator received a configuration")
	}
	if got, err := reader.ResolveForwardingConnection(ctx, "admin-one", proof.RuleID); err == nil || got.URI != "" {
		t.Fatal("paused export weakened live readiness")
	}
	replace([]byte(`{"Version":24,"invalid":true}`))
	if got, err := resolver.ResolveForwardingConfiguration(ctx, "admin-one", proof.RuleID); err == nil || got != (vlessconnection.Configuration{}) {
		t.Fatal("corrupt authority served cached credentials")
	}
	replace(original)
	read()
	identity, err := vlessidentity.NewService(writer, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := identity.CredentialForAdministrator(ctx, "admin-one", proof.RuleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Revoke(ctx, "admin-one", record.Binding.ID, vlessidentity.MutationRequest{Revision: record.Binding.Revision}, "configuration-revoke"); err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.ResolveForwardingConfiguration(ctx, "admin-one", proof.RuleID); err == nil || got != (vlessconnection.Configuration{}) {
		t.Fatal("revoked identity served cached credentials")
	}
	replace(original)
	read() // Warm a valid projection before closing the database.
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.ResolveForwardingConfiguration(ctx, "admin-one", proof.RuleID); err == nil || got != (vlessconnection.Configuration{}) {
		t.Fatal("unavailable database served cached credentials")
	}
}
