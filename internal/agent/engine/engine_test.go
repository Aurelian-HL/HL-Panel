package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestDryRunFileAdapterLifecycle(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	adapter := newTestAdapter(t, directory)
	desired := testDesired(t, 1, []agentv1.ConfigurationFragment{
		{
			GroupID:       "group-a",
			GroupRevision: 2,
			Engine:        agentv1.EngineXray,
			Config:        json.RawMessage(`{"schema_version":1,"services":[]}`),
		},
	})

	prepared, err := adapter.Prepare(context.Background(), desired)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := adapter.Validate(context.Background(), prepared); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := adapter.Commit(context.Background(), prepared); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := adapter.Verify(context.Background(), prepared); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	active, err := os.ReadFile(filepath.Join(directory, "active", "node-bundle.json"))
	if err != nil {
		t.Fatalf("ReadFile(active) error = %v", err)
	}
	if string(active) != string(desired.Config) {
		t.Fatalf("active configuration differs from desired configuration")
	}
}

func TestDryRunFileAdapterRollbackRestoresPreviousConfiguration(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	adapter := newTestAdapter(t, directory)
	firstDesired := testDesired(t, 1, []agentv1.ConfigurationFragment{{
		GroupID:       "group-a",
		GroupRevision: 1,
		Engine:        agentv1.EngineXray,
		Config:        json.RawMessage(`{"services":[]}`),
	}})
	first, err := adapter.Prepare(context.Background(), firstDesired)
	if err != nil {
		t.Fatalf("Prepare(first) error = %v", err)
	}
	if err := adapter.Commit(context.Background(), first); err != nil {
		t.Fatalf("Commit(first) error = %v", err)
	}

	secondDesired := testDesired(t, 2, []agentv1.ConfigurationFragment{{
		GroupID:       "group-a",
		GroupRevision: 2,
		Engine:        agentv1.EngineGOST,
		Config:        json.RawMessage(`{"services":[{"name":"next"}]}`),
	}})
	second, err := adapter.Prepare(context.Background(), secondDesired)
	if err != nil {
		t.Fatalf("Prepare(second) error = %v", err)
	}
	if err := adapter.Commit(context.Background(), second); err != nil {
		t.Fatalf("Commit(second) error = %v", err)
	}
	if err := adapter.Rollback(context.Background(), &first); err != nil {
		t.Fatalf("Rollback(first) error = %v", err)
	}
	if err := adapter.Verify(context.Background(), first); err != nil {
		t.Fatalf("Verify(first after rollback) error = %v", err)
	}

	if err := adapter.Rollback(context.Background(), nil); err != nil {
		t.Fatalf("Rollback(nil) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "active", "node-bundle.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active file after empty rollback error = %v, want os.ErrNotExist", err)
	}
}

func TestPrepareRejectsInvalidBundles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  string
		engine  agentv1.Engine
		wantErr error
	}{
		{
			name:    "unsupported desired engine",
			config:  `{"schema_version":1,"fragments":[]}`,
			engine:  agentv1.EngineXray,
			wantErr: ErrUnsupportedEngine,
		},
		{
			name:    "unknown bundle field",
			config:  `{"schema_version":1,"fragments":[],"unexpected":true}`,
			engine:  agentv1.EngineNodeBundle,
			wantErr: ErrInvalidConfiguration,
		},
		{
			name:    "null fragments",
			config:  `{"schema_version":1,"fragments":null}`,
			engine:  agentv1.EngineNodeBundle,
			wantErr: ErrInvalidConfiguration,
		},
		{
			name:    "unknown fragment engine",
			config:  `{"schema_version":1,"fragments":[{"group_id":"a","group_generation":1,"engine":"shell","config":{}}]}`,
			engine:  agentv1.EngineNodeBundle,
			wantErr: ErrUnsupportedEngine,
		},
		{
			name:    "fragment config is not object",
			config:  `{"schema_version":1,"fragments":[{"group_id":"a","group_generation":1,"engine":"xray","config":[]}]}`,
			engine:  agentv1.EngineNodeBundle,
			wantErr: ErrInvalidConfiguration,
		},
		{
			name:    "duplicate groups",
			config:  `{"schema_version":1,"fragments":[{"group_id":"a","group_generation":1,"engine":"xray","config":{}},{"group_id":"a","group_generation":2,"engine":"gost","config":{}}]}`,
			engine:  agentv1.EngineNodeBundle,
			wantErr: ErrInvalidConfiguration,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			adapter := newTestAdapter(t, directory)
			desired := desiredFromRaw(1, test.engine, []byte(test.config))
			_, err := adapter.Prepare(context.Background(), desired)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Prepare() error = %v, want errors.Is(%v)", err, test.wantErr)
			}
		})
	}
}

func TestPrepareRejectsHashMismatch(t *testing.T) {
	t.Parallel()
	adapter := newTestAdapter(t, t.TempDir())
	desired := desiredFromRaw(1, agentv1.EngineNodeBundle, []byte(`{"schema_version":1,"fragments":[]}`))
	desired.ConfigSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	_, err := adapter.Prepare(context.Background(), desired)
	if !errors.Is(err, ErrConfigurationHash) {
		t.Fatalf("Prepare() error = %v, want ErrConfigurationHash", err)
	}
}

func TestValidateRejectsTamperedStagingFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	adapter := newTestAdapter(t, directory)
	desired := testDesired(t, 1, []agentv1.ConfigurationFragment{})
	prepared, err := adapter.Prepare(context.Background(), desired)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "staging", "node-bundle.json"), []byte(`{"schema_version":1,"fragments":[],"tampered":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile(staging) error = %v", err)
	}
	if err := adapter.Validate(context.Background(), prepared); !errors.Is(err, ErrConfigurationHash) {
		t.Fatalf("Validate() error = %v, want ErrConfigurationHash", err)
	}
}

func newTestAdapter(t *testing.T, directory string) *DryRunFileAdapter {
	t.Helper()
	adapter, err := NewDryRunFileAdapter(
		filepath.Join(directory, "staging", "node-bundle.json"),
		filepath.Join(directory, "active", "node-bundle.json"),
	)
	if err != nil {
		t.Fatalf("NewDryRunFileAdapter() error = %v", err)
	}
	return adapter
}

func testDesired(t *testing.T, generation agentv1.NodeConfigGeneration, fragments []agentv1.ConfigurationFragment) agentv1.DesiredNodeConfig {
	t.Helper()
	config, err := json.Marshal(agentv1.ConfigurationBundle{
		SchemaVersion: BundleSchemaVersion,
		Fragments:     fragments,
	})
	if err != nil {
		t.Fatalf("json.Marshal(bundle) error = %v", err)
	}
	return desiredFromRaw(generation, agentv1.EngineNodeBundle, config)
}

func desiredFromRaw(generation agentv1.NodeConfigGeneration, engine agentv1.Engine, config []byte) agentv1.DesiredNodeConfig {
	sum := sha256.Sum256(config)
	return agentv1.DesiredNodeConfig{
		Generation:   generation,
		Engine:       engine,
		ConfigSHA256: hex.EncodeToString(sum[:]),
		Config:       config,
	}
}
