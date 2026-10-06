package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

func vlessCredentialEditFixture(t *testing.T) (*Store, forwarding.Request, forwarding.Rule, provisioningvless.SOCKS5Upstream) {
	t.Helper()
	store, request := forwardingRepositoryFixture(t)
	request.Name = "authenticated VLESS landing"
	request.IngressProtocol = forwarding.IngressVLESSReality
	request.Targets = nil
	request.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.test"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "edit-landing-user"
	request.VLESSSOCKS5Password = "edit-landing-secret"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = strings.Repeat("A", 43)
	request.RealityShortID = "0123456789abcdef"
	rule, _, err := forwarding.NewService(store, nil).CreateForAdministrator(context.Background(), "admin-test", request, "create-vless-credentials")
	if err != nil {
		t.Fatalf("create VLESS rule: %v", err)
	}
	upstream, ok := store.VLESSSOCKS5Upstream(rule.ID)
	if !ok {
		t.Fatal("new VLESS rule has no private upstream")
	}
	request.Revision = rule.Revision
	request.VLESSSOCKS5Username = ""
	request.VLESSSOCKS5Password = ""
	return store, request, rule, upstream
}

func TestVLESSEditRetainsPrivateUpstreamAcrossReplayAndSnapshot(t *testing.T) {
	store, request, rule, original := vlessCredentialEditFixture(t)
	ctx := context.Background()
	service := forwarding.NewService(store, nil)
	request.Name = "renamed VLESS rule"
	updated, replayed, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "edit-vless-retain")
	if err != nil || replayed || updated.Revision != rule.Revision+1 || updated.ListenPort != rule.ListenPort {
		t.Fatalf("edit without re-entering credentials: replayed=%v err=%v", replayed, err)
	}
	if got, ok := store.VLESSSOCKS5Upstream(rule.ID); !ok || got != original {
		t.Fatal("ordinary edit changed the stored SOCKS5 upstream")
	}
	beforeReplay := len(store.AuditEvents())
	replay, replayed, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "edit-vless-retain")
	if err != nil || !replayed || replay.Revision != updated.Revision || len(store.AuditEvents()) != beforeReplay {
		t.Fatalf("replayed edit appended audit or changed revision: %v", err)
	}
	public, err := json.Marshal([]forwarding.Rule{updated, replay})
	if err != nil {
		t.Fatal(err)
	}
	audits, err := json.Marshal(store.AuditEvents())
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range [][]byte{public, audits} {
		if bytes.Contains(output, []byte(original.Username)) || bytes.Contains(output, []byte(original.Password)) {
			t.Fatal("retained SOCKS5 credentials leaked into a public projection or audit")
		}
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("edited VLESS rule snapshot rejected: %v", err)
	}
	request.Revision = updated.Revision
	request.Description = "edited after restart"
	// Older clients echo the original username but still omit its password.
	request.VLESSSOCKS5Username = original.Username
	if _, _, err := forwarding.NewService(restored, nil).UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "edit-vless-after-restart"); err != nil {
		t.Fatalf("restart lost ability to retain upstream credentials: %v", err)
	}
	if got, ok := restored.VLESSSOCKS5Upstream(rule.ID); !ok || got != original {
		t.Fatal("restart changed retained SOCKS5 credentials")
	}
}

func TestVLESSCredentialOmissionRejectsChangedOrUnavailableUpstreamWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Store, *forwarding.Request, string)
	}{
		{name: "changed host", change: func(_ *Store, request *forwarding.Request, _ string) { request.VLESSSOCKS5Host = "other.example.test" }},
		{name: "changed port", change: func(_ *Store, request *forwarding.Request, _ string) { request.VLESSSOCKS5Port++ }},
		{name: "changed username without password", change: func(_ *Store, request *forwarding.Request, _ string) { request.VLESSSOCKS5Username = "other-user" }},
		{name: "missing stored upstream", change: func(store *Store, _ *forwarding.Request, id string) { delete(store.vlessSOCKS5Upstreams, id) }},
		{name: "mismatched stored upstream", change: func(store *Store, _ *forwarding.Request, id string) {
			upstream := store.vlessSOCKS5Upstreams[id]
			upstream.Port++
			store.vlessSOCKS5Upstreams[id] = upstream
		}},
		{name: "empty stored credentials", change: func(store *Store, _ *forwarding.Request, id string) {
			upstream := store.vlessSOCKS5Upstreams[id]
			upstream.Username, upstream.Password = "", ""
			store.vlessSOCKS5Upstreams[id] = upstream
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, request, rule, original := vlessCredentialEditFixture(t)
			test.change(store, &request, rule.ID)
			before := store.forwardRules[rule.ID]
			beforeUpstream, hadUpstream := store.VLESSSOCKS5Upstream(rule.ID)
			beforeAudits := len(store.AuditEvents())
			beforeIdempotency := len(store.businessIdempotency)
			_, _, err := forwarding.NewService(store, nil).UpdateForAdministrator(context.Background(), "admin-test", rule.ID, request, "edit-invalid-upstream")
			if !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("invalid credential omission was accepted: %v", err)
			}
			if strings.Contains(err.Error(), original.Username) || strings.Contains(err.Error(), original.Password) {
				t.Fatal("credential error disclosed private material")
			}
			afterUpstream, hasUpstream := store.VLESSSOCKS5Upstream(rule.ID)
			if !reflect.DeepEqual(store.forwardRules[rule.ID], before) || hadUpstream != hasUpstream || beforeUpstream != afterUpstream ||
				len(store.AuditEvents()) != beforeAudits || len(store.businessIdempotency) != beforeIdempotency {
				t.Fatal("rejected credential update changed stored rule, secret, audit or idempotency state")
			}
		})
	}
}

func TestVLESSCompleteCredentialsReplaceUpstreamAndOwnerCheckRemainsEnforced(t *testing.T) {
	store, request, rule, original := vlessCredentialEditFixture(t)
	ctx := context.Background()
	service := forwarding.NewService(store, nil)
	if _, _, err := service.UpdateForAdministrator(ctx, "other-admin", rule.ID, request, "edit-vless-other-owner"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("another administrator edited the VLESS rule: %v", err)
	}
	if got, _ := store.VLESSSOCKS5Upstream(rule.ID); got != original {
		t.Fatal("unauthorized edit changed private credentials")
	}
	request.VLESSSOCKS5Host = "replacement.example.test"
	request.VLESSSOCKS5Port = 2080
	request.VLESSSOCKS5Username = "replacement-user"
	request.VLESSSOCKS5Password = "replacement-secret"
	updated, _, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "edit-vless-replacement")
	if err != nil || updated.VLESSSOCKS5Host != request.VLESSSOCKS5Host || updated.VLESSSOCKS5Port != request.VLESSSOCKS5Port {
		t.Fatalf("complete replacement credentials were rejected: %v", err)
	}
	want := provisioningvless.SOCKS5Upstream{Hostname: request.VLESSSOCKS5Host, Port: request.VLESSSOCKS5Port, Username: request.VLESSSOCKS5Username, Password: request.VLESSSOCKS5Password}
	if got, ok := store.VLESSSOCKS5Upstream(rule.ID); !ok || got != want {
		t.Fatal("complete replacement did not persist the new upstream")
	}
}

func TestVLESSRepositoryCreationCannotReuseCredentialOmission(t *testing.T) {
	store, _, rule, _ := vlessCredentialEditFixture(t)
	rule.ID = "fwd-missing-credentials"
	rule.ListenPort = 0
	rule.Revision = 1
	beforeRules := len(store.forwardRules)
	beforeAudits := len(store.AuditEvents())
	beforeIdempotency := len(store.businessIdempotency)
	_, _, err := store.CreateForwardingRule(context.Background(), forwarding.CreateInput{
		Rule: rule, CreatedBy: "admin-test", IdempotencyKey: "direct-empty-create", RequestSHA256: strings.Repeat("a", 64),
	}, audit.Event{})
	if !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("repository created a VLESS upstream without credentials: %v", err)
	}
	if len(store.forwardRules) != beforeRules || len(store.AuditEvents()) != beforeAudits || len(store.businessIdempotency) != beforeIdempotency {
		t.Fatal("rejected VLESS creation mutated business state")
	}
	if _, ok := store.VLESSSOCKS5Upstream(rule.ID); ok {
		t.Fatal("rejected VLESS creation retained an empty credential record")
	}
}
