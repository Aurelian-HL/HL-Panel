package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func TestNativeCredentialsRotationPreservesIdentityAndIsAtomic(t *testing.T) {
	f, p := nativeRuntimeFixture(t)
	var config map[string]any
	json.Unmarshal(p.Config, &config)
	config["outbounds"].([]any)[0] = map[string]any{"tag": "direct", "protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": "proxy.example.org", "port": 1080, "users": []any{map[string]any{"user": "old-user", "pass": "old-secret"}}}}}}
	p.Config, _ = json.Marshal(config)
	svc, _ := vlessruntime.NewService(f.store, time.Now)
	m, _, err := svc.BindImported(context.Background(), "admin", f.bindingID, f.nodeID, p.Config, "credential-fixture")
	if err != nil {
		t.Fatal(err)
	}
	request := requestFromNative(f.rule)
	request.Paused = false
	f.rule, _, err = forwarding.NewService(f.store, time.Now).Update(context.Background(), "admin", f.rule.ID, request, "activate-fixture")
	if err != nil {
		t.Fatal(err)
	}
	in := vlessruntime.NativeCredentialsRotation{AdministratorID: "admin", RuleID: f.rule.ID, ExpectedRevision: f.rule.Revision, MaterialRevisions: map[string]int64{m.ID: 1}, Host: "proxy.example.org", Port: 1080, Username: "new-user", Password: "new-secret", IdempotencyKey: "test-native-rotation"}
	before, _ := f.store.EncodeSnapshot()
	bad := in
	bad.AdministratorID = "unknown"
	if _, err = f.store.RotateNativeSOCKS5Credentials(context.Background(), bad); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("authorization", err)
	}
	bad = in
	bad.Host = "different.example.org"
	if _, err = f.store.RotateNativeSOCKS5Credentials(context.Background(), bad); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("target match", err)
	}
	bad = in
	bad.ExpectedRevision++
	if _, err = f.store.RotateNativeSOCKS5Credentials(context.Background(), bad); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("CAS", err)
	}
	afterFailures, _ := f.store.EncodeSnapshot()
	if string(before) != string(afterFailures) {
		t.Fatal("failed operation mutated state")
	}
	result, err := f.store.RotateNativeSOCKS5Credentials(context.Background(), in)
	if err != nil || result.Materials != 1 || result.Revision != in.ExpectedRevision+1 {
		t.Fatal(result, err)
	}
	material, err := f.store.RuntimeMaterialForNode(context.Background(), f.nodeID, f.bindingID)
	if err != nil {
		t.Fatal(err)
	}
	var updated map[string]any
	json.Unmarshal(material.NativeConfig, &updated)
	if !reflect.DeepEqual(config["inbounds"], updated["inbounds"]) || !reflect.DeepEqual(config["routing"], updated["routing"]) || material.CredentialUUID != p.UUID || material.RealityPrivateKey != p.PrivateKey {
		t.Fatal("identity/route changed")
	}
	user := updated["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["pass"] != "new-secret" || user["user"] != "new-user" {
		t.Fatal("credentials not changed")
	}
	if again, err := f.store.RotateNativeSOCKS5Credentials(context.Background(), in); err != nil || !again.Replayed {
		t.Fatal("retry", again, err)
	}
	bad = in
	bad.Password = "other-secret"
	if _, err = f.store.RotateNativeSOCKS5Credentials(context.Background(), bad); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("conflicting retry", err)
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeSnapshot(raw); err != nil {
		t.Fatal("restore", err)
	}
	fragments, err := f.store.compileForwardingFragmentsLocked(f.nodeID)
	if err != nil || len(fragments) != 1 || !strings.Contains(string(fragments[0].Config), "new-secret") {
		t.Fatal("updated bundle", err)
	}
	for _, event := range f.store.AuditEvents() {
		b, _ := json.Marshal(event)
		if stringContainsAny(string(b), "new-secret", "old-secret", "new-user") {
			t.Fatal("audit leaked credentials")
		}
	}
}

func stringContainsAny(s string, values ...string) bool {
	for _, v := range values {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

func TestNativeCredentialsRejectsAmbiguousServer(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[{"protocol":"socks","settings":{"servers":[{"address":"proxy","port":1080,"users":[{"user":"x","pass":"y"}]},{"address":"proxy","port":1080,"users":[{"user":"x","pass":"y"}]}]}}]}`)
	if _, err := rotateNativeSOCKS5JSON(raw, "proxy", 1080, "new", "secret"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal(err)
	}
}
