package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
	"github.com/hongle/hl-panel/internal/control/panelquota"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
)

func panelQuotaLimits() panelquota.Limits { return panelquota.Limits{DeviceGroups: 1, Rules: 10} }
func panelQuotaSeed(t *testing.T, s *Store) forwarding.Request {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	event := audit.Event{ID: "quota-seed", ActorType: "administrator", ActorID: "admin-one", Action: "test", Outcome: "succeeded", CreatedAt: now}
	if err := s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "entry", Name: "entry", Kind: groups.KindEntry, CreatedAt: now}, event); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.UpdateGroupNetwork(ctx, groupconfig.UpdateInput{Network: groupconfig.GroupNetwork{GroupID: "entry", ConnectHost: "127.0.0.1", PortStart: 12000, PortEnd: 12100, AllowDirect: true, TrafficMultiplier: 1, Revision: 1, UpdatedAt: now}, CreatedBy: "admin-one", IdempotencyKey: "net", RequestSHA256: "net"}, event)
	if err != nil {
		t.Fatal(err)
	}
	return forwarding.Request{Name: "test", EntryGroupID: "entry", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 19001}}, SelectionPolicy: forwarding.SelectionRoundRobin}
}

func TestPostgreSQLPanelQuotaAcrossStoresAndBulk(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDatabaseURL(t)
	admin := bootstrapAdministrator(t)
	first, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	first.SetPanelQuotaProvider(panelQuotaLimits)
	second.SetPanelQuotaProvider(panelQuotaLimits)
	request := panelQuotaSeed(t, first)
	if err := second.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "second", Name: "second", Kind: groups.KindEntry}, audit.Event{}); !errors.Is(err, panelquota.ErrExceeded) {
		t.Fatalf("second process group expansion: %v", err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	winners := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := first
			if i%2 == 1 {
				s = second
			}
			r := request
			r.Paused = i%2 == 0
			key := fmt.Sprintf("quota-%d", i)
			_, _, err := forwarding.NewService(s, nil).CreateForAdministrator(ctx, admin.ID, r, key)
			if err == nil {
				successes.Add(1)
				winners <- key
			} else if !errors.Is(err, panelquota.ErrExceeded) {
				t.Errorf("unexpected concurrent error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	if successes.Load() != 10 {
		t.Fatalf("cross-store quota created %d", successes.Load())
	}
	// Use the request associated with the winning key for an exact replay.
	key := <-winners
	var index int
	fmt.Sscanf(key, "quota-%d", &index)
	r := request
	r.Paused = index%2 == 0
	if _, replay, err := forwarding.NewService(second, nil).CreateForAdministrator(ctx, admin.ID, r, key); err != nil || !replay {
		t.Fatalf("replay at capacity: %v", err)
	}
	usage, err := second.PanelResourceUsage(ctx)
	if err != nil || usage.Rules != 10 || usage.DeviceGroups != 1 {
		t.Fatalf("usage %+v %v", usage, err)
	}
	// Restart preserves all ten rules and reattaches the local entitlement provider.
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.SetPanelQuotaProvider(panelQuotaLimits)
	before, err := reopened.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transfer := forwarding.TransferRequest{Format: forwarding.ImportFormatNYText, Content: "extra##127.0.0.1#19001", Mapping: forwarding.ImportMapping{EntryGroupID: "entry", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, SelectionPolicy: forwarding.SelectionRoundRobin}}
	if _, _, err := forwarding.NewService(reopened, nil).ImportForAdministrator(ctx, admin.ID, transfer, "bulk-at-full"); !errors.Is(err, panelquota.ErrExceeded) {
		t.Fatalf("bulk bypassed global quota: %v", err)
	}
	after, err := reopened.AuditEvents(ctx)
	if err != nil || len(after) != len(before) {
		t.Fatal("rejected bulk left audit changes")
	}
}

func TestPostgreSQLPanelQuotaRestoreCannotExpandAndKeepsRecoveryUntouched(t *testing.T) {
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migration, err := usagemigration.New(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migration.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	request := panelQuotaSeed(t, s)
	service := forwarding.NewService(s, nil)
	for i := 0; i < 11; i++ {
		request.Paused = i%2 == 0
		if _, _, err = service.CreateForAdministrator(ctx, admin.ID, request, fmt.Sprintf("restore-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	existing, err := s.ExportMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Decode and startup keep existing over-limit content. A later restore to an
	// empty target must not recreate eleven rules under a free entitlement.
	target, err := Open(ctx, isolatedDatabaseURL(t), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	migration, err = usagemigration.New(target.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migration.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	target.SetPanelQuotaProvider(panelQuotaLimits)
	before, err := target.ExportMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recoveryCalls := 0
	bundle := migrationbackup.Bundle{Manifest: migrationbackup.Manifest{Format: 1, Version: "v0.1.116", CreatedAt: time.Now().UTC(), SourceURL: "https://source.example.test"}, State: existing, Runtime: migrationbackup.RuntimeSecrets{PasswordFingerprintKey: bytes.Repeat([]byte{7}, 32)}}
	if err = migrationbackup.Validate(bundle, bundle.Manifest.Version); err != nil {
		t.Fatalf("restore fixture is invalid: %v", err)
	}
	_, err = target.RestoreMigration(ctx, migrationbackup.RestoreInput{Bundle: bundle, AdministratorID: admin.ID, AdministratorHash: admin.PasswordHash, Key: "quota-restore", Digest: strings.Repeat("a", 64), SaveRecovery: func(migrationbackup.State) (string, error) { recoveryCalls++; return "must-not-be-written", nil }})
	if !errors.Is(err, panelquota.ErrExceeded) || recoveryCalls != 0 {
		t.Fatalf("restore failed too late or bypassed: %v recovery=%d", err, recoveryCalls)
	}
	after, err := target.ExportMigration(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed restore altered target")
	}
	s.SetPanelQuotaProvider(panelQuotaLimits)
	items, err := s.ListForwardingRules(ctx)
	if err != nil || len(items) != 11 {
		t.Fatal("existing over-limit rules lost")
	}
	request.Revision = items[0].Revision
	request.Name = "maintained"
	request.Paused = items[0].Paused
	if _, _, err = service.UpdateForAdministrator(ctx, admin.ID, items[0].ID, request, "over-limit-maintenance"); err != nil {
		t.Fatal(err)
	}
	// Trusted snapshots contain neither entitlement maxima nor unsigned tier overrides.
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(existing.Snapshot, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope["panel_quota"]; ok {
		t.Fatal("quota serialized into untrusted snapshot")
	}
}
