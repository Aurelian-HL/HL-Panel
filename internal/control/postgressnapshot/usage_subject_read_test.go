package postgressnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

type usageSubjectStoredBinding struct {
	Binding        vlessidentity.Binding
	CredentialUUID string
}

// Synthetic storage pressure, not a copy of confidential production state.
// Session padding matches payload size; it does not model native Xray configs.
func usageSubjectFixture(t testing.TB, padded bool) (*memoryrepo.Store, []byte) {
	t.Helper()
	_, base := snapshotReadFixture(t, 0)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(base, &doc); err != nil {
		t.Fatal(err)
	}
	put := func(key string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		doc[key] = raw
	}
	rules := make(map[string]forwarding.Rule)
	deviceGroups := make(map[string]groups.DeviceGroup)
	networks := make(map[string]groupconfig.GroupNetwork)
	pools := make(map[string]endpoints.EndpointPool)
	members := make(map[string]map[string]groups.Member)
	revisionKeys := make(map[string]map[string]string)
	endpointMembers := make(map[string]map[string]endpoints.EndpointPoolMember)
	bindings := make(map[string]usageSubjectStoredBinding)
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 245; i++ {
		id, group := fmt.Sprintf("usage-rule-%03d", i), fmt.Sprintf("usage-entry-%03d", i)
		deviceGroups[group] = groups.DeviceGroup{ID: group, Name: group, Kind: groups.KindEntry, CreatedAt: now}
		members[group] = map[string]groups.Member{}
		revisionKeys[group] = map[string]string{}
		networks[group] = groupconfig.GroupNetwork{GroupID: group, ConnectHost: "127.0.0.1", PortStart: 12000, PortEnd: 13000, AllowDirect: true, TrafficMultiplier: 1, Revision: 1, UpdatedAt: now}
		rules[id] = forwarding.Rule{ID: id, Name: id, Revision: 1, OwnerKind: forwarding.OwnerUnassigned,
			CustomerID: "klunassigned_" + group, EntryGroupID: group, VLESSOutboundMode: forwarding.VLESSOutboundNative,
			IngressProtocol: forwarding.IngressVLESSReality, EgressMode: forwarding.EgressDirect,
			Protocol: forwarding.ProtocolTCP, ListenPort: 12000 + i, CreatedAt: now, UpdatedAt: now,
			VLESSFlowExplicit: true, RealityServerName: "example.test", RealityShortID: "a1", RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", SelectionPolicy: forwarding.SelectionRoundRobin}
		poolID, bindingID := "pool-"+id, "binding-"+id
		endpointMembers[poolID] = map[string]endpoints.EndpointPoolMember{}
		pools[poolID] = endpoints.EndpointPool{ID: poolID, Name: poolID, GroupID: group, RuleID: id,
			Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless", Hostname: "127.0.0.1", Port: 12000 + i,
			SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now, UpdatedAt: now}
		bindings[bindingID] = usageSubjectStoredBinding{Binding: vlessidentity.Binding{ID: bindingID,
			CustomerID: rules[id].CustomerID, ForwardingRuleID: id, EndpointPoolID: poolID,
			State: vlessidentity.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
			ImportedFrom: &vlessidentity.ImportOrigin{BatchID: "synthetic-usage-batch", LineID: int64(i + 1)}},
			CredentialUUID: "00000000-0000-4000-8000-000000000001"}
	}
	put("ForwardRules", rules)
	put("DeviceGroups", deviceGroups)
	put("GroupNetworks", networks)
	put("EndpointPools", pools)
	put("VLESSBindings", bindings)
	put("MembersByGroup", members)
	put("RevisionByIdempotency", revisionKeys)
	put("EndpointMembers", endpointMembers)
	if padded {
		sessions := make(map[string]auth.Session, 22000)
		for i := 0; i < 22000; i++ {
			id := fmt.Sprintf("usage-padding-session-%05d", i)
			sessions[fmt.Sprintf("%064x", i)] = auth.Session{ID: id, AdminID: "cache-admin", TokenHash: fmt.Sprintf("%064x", i), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		}
		put("Sessions", sessions)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	state, err := memoryrepo.DecodeSnapshotAtVersion(raw, memoryrepo.SnapshotVersion)
	if err != nil {
		t.Fatal(err)
	}
	return state, raw
}

func TestUsageSubjectCacheRevalidatesDatabaseIdentity(t *testing.T) {
	ctx, dsn := context.Background(), isolatedDatabaseURL(t)
	writer, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	_, raw := usageSubjectFixture(t, false)
	replace := func(raw []byte, bump bool) {
		t.Helper()
		query := `UPDATE nyvp_control_snapshots SET payload=$1`
		if bump {
			query += `,revision=revision+1`
		}
		if _, err := writer.db.ExecContext(ctx, query, raw); err != nil {
			t.Fatal(err)
		}
	}
	subject := "klunassigned_usage-entry-000"
	replace(raw, true)
	if err := reader.ValidateUnassignedSubject(ctx, subject); err != nil {
		t.Fatal(err)
	}
	cached := reader.readCache.usage
	if cached == nil {
		t.Fatal("subject read did not populate detached projection")
	}
	if err := reader.ValidateUnassignedSubject(ctx, subject); err != nil || reader.readCache.usage != cached {
		t.Fatal("warm projection not reused", err)
	}
	if err := reader.ValidateUnassignedSubject(ctx, "unknown-subject"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("unknown subject accepted", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// Repair without increasing revision must revoke the cached authorization.
	doc["ForwardRules"] = json.RawMessage(`{}`)
	doc["EndpointPools"] = json.RawMessage(`{}`)
	doc["VLESSBindings"] = json.RawMessage(`{}`)
	empty, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	replace(empty, false)
	if err := reader.ValidateUnassignedSubject(ctx, subject); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("same-revision revocation hidden", err)
	}
	replace(raw, true)
	if err := reader.ValidateUnassignedSubject(ctx, subject); err != nil {
		t.Fatal("other-writer restoration hidden", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := reader.ValidateUnassignedSubject(canceled, subject); err == nil {
		t.Fatal("canceled validation used cached authorization")
	}
	replace([]byte(`{"Version":24}`), false)
	if err := reader.ValidateUnassignedSubject(ctx, subject); err == nil || errors.Is(err, faults.ErrNotFound) {
		t.Fatal("corrupt database not surfaced", err)
	}
	reader.Close()
	if err := reader.ValidateUnassignedSubject(ctx, subject); err == nil {
		t.Fatal("closed database used cached authorization")
	}
}

func TestUsageSubjectAdministratorRequiresImportProvenance(t *testing.T) {
	ctx, dsn := context.Background(), isolatedDatabaseURL(t)
	store, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, raw := usageSubjectFixture(t, false)
	for _, tc := range []struct {
		name                         string
		baseline, poolOwner, allowed bool
	}{
		{"imported_administrator", true, true, true},
		{"native_flag_without_baseline", false, true, false},
		{"import_without_pool_management", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]json.RawMessage
			var rules map[string]forwarding.Rule
			var pools map[string]endpoints.EndpointPool
			var bindings map[string]usageSubjectStoredBinding
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(doc["ForwardRules"], &rules); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(doc["EndpointPools"], &pools); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(doc["VLESSBindings"], &bindings); err != nil {
				t.Fatal(err)
			}
			rule := rules["usage-rule-000"]
			rule.OwnerKind, rule.OwnerID = forwarding.OwnerAdministrator, "cache-admin"
			rule.CustomerID = forwarding.AdministratorSubjectID(rule.OwnerID)
			if tc.baseline {
				rule.ImportedTraffic = forwarding.ImportedTraffic{SourceID: "synthetic-source", BatchID: "synthetic-usage-batch", LineID: 1, ObservedAt: rule.CreatedAt}
			}
			rules[rule.ID] = rule
			pool := pools["pool-usage-rule-000"]
			if tc.poolOwner {
				pool.OwnerID = rule.OwnerID
			}
			pools[pool.ID] = pool
			binding := bindings["binding-usage-rule-000"]
			binding.Binding.CustomerID = rule.CustomerID
			bindings[binding.Binding.ID] = binding
			put := func(key string, value any) {
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				doc[key] = encoded
			}
			put("ForwardRules", rules)
			put("EndpointPools", pools)
			put("VLESSBindings", bindings)
			payload, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			state, err := memoryrepo.DecodeSnapshotAtVersion(payload, memoryrepo.SnapshotVersion)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1`, payload); err != nil {
				t.Fatal(err)
			}
			for name, validate := range map[string]func(context.Context, string) error{
				"original_store":       state.ValidateUnassignedSubject,
				"validated_projection": store.ValidateUnassignedSubject,
			} {
				err := validate(ctx, rule.CustomerID)
				if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, faults.ErrNotFound) {
					t.Fatalf("%s authorization mismatch: %v", name, err)
				}
			}
		})
	}
}

func BenchmarkUsageSubjectRead(b *testing.B) {
	state, raw := usageSubjectFixture(b, true)
	view, ctx := state.UsageReadView(), context.Background()
	b.Logf("synthetic rules=245 snapshot_bytes=%d; padding consists of sessions, not native runtime", len(raw))
	b.Run("decode_then_subject", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s, err := decodeReadSnapshot(raw, memoryrepo.SnapshotVersion, 3)
			if err != nil {
				b.Fatal(err)
			}
			if err = s.ValidateUnassignedSubject(ctx, "klunassigned_usage-entry-000"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm_subject_projection", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := view.ValidateUnassignedSubject(ctx, "klunassigned_usage-entry-000"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm_after_ingest_rule_lookup", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := view.RuleTrafficState(ctx, "usage-rule-000"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cold_snapshot_and_all_views", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s, err := decodeReadSnapshot(raw, memoryrepo.SnapshotVersion, 3)
			if err != nil {
				b.Fatal(err)
			}
			if s.IdentityReadView() == nil || s.UsageReadView() == nil || s.ForwardingReadView() == nil {
				b.Fatal("missing projection")
			}
		}
	})
}

func TestUsageSubjectAndTrafficReadCostOnIsolatedPostgres(t *testing.T) {
	ctx, dsn := context.Background(), isolatedDatabaseURL(t)
	store, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, raw := usageSubjectFixture(t, true)
	if _, err = store.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1`, raw); err != nil {
		t.Fatal(err)
	}
	subject := "klunassigned_usage-entry-000"
	if err = store.ValidateUnassignedSubject(ctx, subject); err != nil {
		t.Fatal(err)
	}
	measure := func(name string, operation func() error) {
		t.Helper()
		started := time.Now()
		for i := 0; i < 25; i++ {
			if err := operation(); err != nil {
				t.Fatal(name, err)
			}
		}
		t.Logf("%s iterations=25 mean=%s snapshot_bytes=%d", name, time.Since(started)/25, len(raw))
	}
	measure("previous_subject_transaction", func() error {
		_, err := transact(ctx, store, false, func(s *memoryrepo.Store) (struct{}, error) {
			return struct{}{}, s.ValidateUnassignedSubject(ctx, subject)
		})
		return err
	})
	measure("validated_subject_projection", func() error { return store.ValidateUnassignedSubject(ctx, subject) })
	total := int64(0)
	measure("after_ingest_state_and_counter_projection", func() error {
		r, err := store.RuleTrafficState(ctx, "usage-rule-000")
		if err != nil {
			return err
		}
		total++
		return store.ApplyRuleTraffic(ctx, forwarding.RuleTrafficProjection{RuleID: r.ID, ExpectedRevision: r.Revision, TotalBytes: total, At: time.Now().UTC()})
	})
}
