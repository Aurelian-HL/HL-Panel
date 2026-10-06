package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestXrayProcessAdapterLifecycleAndRollback(t *testing.T) {
	t.Parallel()
	runner := &recordingXrayRunner{}
	adapter := newTestXrayAdapter(t, runner, true)
	first := xrayDesired(t, 1, []agentv1.ConfigurationFragment{{
		GroupID: "group-a", GroupRevision: 1, Engine: agentv1.EngineXray,
		Config: json.RawMessage(`{"log":{"loglevel":"warning"},"inbounds":[{"tag":"in-a","listen":"127.0.0.1","port":18081,"protocol":"vless"}],"outbounds":[{"tag":"out-a","protocol":"freedom"}]}`),
	}})
	preparedFirst, err := adapter.Prepare(context.Background(), first)
	if err != nil {
		t.Fatalf("Prepare(first) error = %v", err)
	}
	if err := adapter.Validate(context.Background(), preparedFirst); err != nil {
		t.Fatalf("Validate(first) error = %v", err)
	}
	if err := adapter.Commit(context.Background(), preparedFirst); err != nil {
		t.Fatalf("Commit(first) error = %v", err)
	}
	if err := adapter.Verify(context.Background(), preparedFirst); err != nil {
		t.Fatalf("Verify(first) error = %v", err)
	}
	if len(runner.starts) != 1 || len(runner.tests) != 2 {
		t.Fatalf("runner calls after first apply: tests=%d starts=%d, want tests=2 starts=1", len(runner.tests), len(runner.starts))
	}

	second := xrayDesired(t, 2, []agentv1.ConfigurationFragment{{
		GroupID: "group-b", GroupRevision: 1, Engine: agentv1.EngineXray,
		Config: json.RawMessage(`{"inbounds":[{"tag":"in-b","listen":"127.0.0.1","port":18082,"protocol":"vless"}],"outbounds":[{"tag":"out-b","protocol":"freedom"}]}`),
	}})
	preparedSecond, err := adapter.Prepare(context.Background(), second)
	if err != nil {
		t.Fatalf("Prepare(second) error = %v", err)
	}
	if err := adapter.Commit(context.Background(), preparedSecond); err != nil {
		t.Fatalf("Commit(second) error = %v", err)
	}
	if err := adapter.Verify(context.Background(), preparedSecond); err != nil {
		t.Fatalf("Verify(second) error = %v", err)
	}
	if runner.processes[0].stopCount != 1 {
		t.Fatalf("first process stop count = %d, want 1", runner.processes[0].stopCount)
	}

	if err := adapter.Rollback(context.Background(), &preparedFirst); err != nil {
		t.Fatalf("Rollback(first) error = %v", err)
	}
	if err := adapter.Verify(context.Background(), preparedFirst); err != nil {
		t.Fatalf("Verify(first after rollback) error = %v", err)
	}
	active, err := os.ReadFile(adapter.activePath)
	if err != nil {
		t.Fatalf("ReadFile(active) error = %v", err)
	}
	expected, err := compileXrayBundle(preparedFirst.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != string(expected) {
		t.Fatalf("active configuration after rollback differs from first")
	}
}

func TestXrayProcessAdapterRejectsMixedEngineBundle(t *testing.T) {
	t.Parallel()
	adapter := newTestXrayAdapter(t, &recordingXrayRunner{}, false)
	desired := xrayDesired(t, 1, []agentv1.ConfigurationFragment{
		{GroupID: "xray", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[],"outbounds":[]}`)},
		{GroupID: "gost", GroupRevision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[]}`)},
	})
	_, err := adapter.Prepare(context.Background(), desired)
	if !errors.Is(err, ErrXrayUnsupportedFragment) {
		t.Fatalf("Prepare() error = %v, want ErrXrayUnsupportedFragment", err)
	}
}

func TestXrayProcessAdapterRejectsConflictingFragmentsAndDuplicateTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		fragments []agentv1.ConfigurationFragment
	}{
		{
			name: "singleton conflict",
			fragments: []agentv1.ConfigurationFragment{
				{GroupID: "a", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[]}`)},
				{GroupID: "b", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"log":{"loglevel":"debug"},"inbounds":[],"outbounds":[]}`)},
			},
		},
		{
			name: "duplicate tag",
			fragments: []agentv1.ConfigurationFragment{
				{GroupID: "a", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[{"tag":"same"}],"outbounds":[]}`)},
				{GroupID: "b", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[{"tag":"same"}],"outbounds":[]}`)},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestXrayAdapter(t, &recordingXrayRunner{}, false)
			_, err := adapter.Prepare(context.Background(), xrayDesired(t, 1, test.fragments))
			if !errors.Is(err, ErrXrayConfigConflict) {
				t.Fatalf("Prepare() error = %v, want ErrXrayConfigConflict", err)
			}
		})
	}
}

func TestXrayProcessAdapterValidationDoesNotEchoSecretsAndDetectsTampering(t *testing.T) {
	t.Parallel()
	runner := &recordingXrayRunner{testErr: errors.New("private-key-secret-and-uuid")}
	adapter := newTestXrayAdapter(t, runner, false)
	desired := xrayDesired(t, 1, []agentv1.ConfigurationFragment{{
		GroupID: "secret-group", GroupRevision: 1, Engine: agentv1.EngineXray,
		Config: json.RawMessage(`{"inbounds":[{"tag":"secret-tag","settings":{"privateKey":"private-key-secret"}}],"outbounds":[]}`),
	}})
	prepared, err := adapter.Prepare(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(context.Background(), prepared); !errors.Is(err, ErrXrayValidation) {
		t.Fatalf("Validate() error = %v, want ErrXrayValidation", err)
	} else if strings.Contains(err.Error(), "private-key-secret") || strings.Contains(err.Error(), "uuid") {
		t.Fatalf("Validate() disclosed a runtime secret: %v", err)
	}

	runner.testErr = nil
	if err := os.WriteFile(prepared.adapterPath, []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(context.Background(), prepared); !errors.Is(err, ErrConfigurationHash) {
		t.Fatalf("Validate(tampered) error = %v, want ErrConfigurationHash", err)
	}
	if len(runner.tests) != 1 {
		t.Fatalf("runner Test called %d times after tampering, want 1", len(runner.tests))
	}
}

func TestXrayProcessAdapterStartFailureRestoresPreviousProcessAndFile(t *testing.T) {
	t.Parallel()
	runner := &recordingXrayRunner{}
	adapter := newTestXrayAdapter(t, runner, true)
	first := xrayDesired(t, 1, []agentv1.ConfigurationFragment{{
		GroupID: "a", GroupRevision: 1, Engine: agentv1.EngineXray,
		Config: json.RawMessage(`{"inbounds":[{"tag":"first"}],"outbounds":[]}`),
	}})
	preparedFirst, err := adapter.Prepare(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), preparedFirst); err != nil {
		t.Fatal(err)
	}
	runner.startErrors = []error{errors.New("candidate failed")}
	second := xrayDesired(t, 2, []agentv1.ConfigurationFragment{{
		GroupID: "b", GroupRevision: 1, Engine: agentv1.EngineXray,
		Config: json.RawMessage(`{"inbounds":[{"tag":"second"}],"outbounds":[]}`),
	}})
	preparedSecond, err := adapter.Prepare(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), preparedSecond); !errors.Is(err, ErrXrayStart) {
		t.Fatalf("Commit(second) error = %v, want ErrXrayStart", err)
	}
	if err := adapter.Verify(context.Background(), preparedFirst); err != nil {
		t.Fatalf("Verify(previous after failed start) error = %v", err)
	}
	if runner.processes[0].stopCount != 1 {
		t.Fatalf("previous process stop count = %d, want 1", runner.processes[0].stopCount)
	}
	if len(runner.processes) != 2 {
		t.Fatalf("process starts = %d, want candidate failure plus previous restart", len(runner.processes))
	}
}

func TestCompileXrayBundleCanonicalizesAndMergesArrays(t *testing.T) {
	bundle := agentv1.ConfigurationBundle{
		SchemaVersion: BundleSchemaVersion,
		Fragments: []agentv1.ConfigurationFragment{
			{GroupID: "b", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"outbounds":[{"protocol":"freedom","tag":"out-b"}],"inbounds":[]}`)},
			{GroupID: "a", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[{"protocol":"vless","tag":"in-a"}],"outbounds":[]}`)},
		},
	}
	compiled, err := compileXrayBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(compiled, &document); err != nil {
		t.Fatal(err)
	}
	var inbounds, outbounds []json.RawMessage
	if err := json.Unmarshal(document["inbounds"], &inbounds); err != nil || len(inbounds) != 2 {
		t.Fatalf("inbounds = %s, error = %v", document["inbounds"], err)
	}
	if err := json.Unmarshal(document["outbounds"], &outbounds); err != nil || len(outbounds) != 2 {
		t.Fatalf("outbounds = %s, error = %v", document["outbounds"], err)
	}
}

func TestCompileXrayBundleKeepsEachInboundOnItsOwnOutbound(t *testing.T) {
	bundle := agentv1.ConfigurationBundle{
		SchemaVersion: BundleSchemaVersion,
		Fragments: []agentv1.ConfigurationFragment{
			{GroupID: "rule-a", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[{"tag":"in-a"}],"outbounds":[{"tag":"out-a"}],"routing":{"rules":[{"type":"field","inboundTag":["in-a"],"outboundTag":"out-a"}]}}`)},
			{GroupID: "rule-b", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[{"tag":"in-b"}],"outbounds":[{"tag":"out-b"}],"routing":{"rules":[{"type":"field","inboundTag":["in-b"],"outboundTag":"out-b"}]}}`)},
		},
	}
	compiled, err := CompileXrayBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(compiled, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Inbounds) != 3 || len(document.Outbounds) != 3 || len(document.Routing.Rules) != 3 {
		t.Fatalf("incomplete merged routes: %s", compiled)
	}
	if document.Inbounds[2].Tag != "hl-stats-api-in" || document.Outbounds[2].Tag != "hl-stats-api" {
		t.Fatalf("stats entries missing from merged bundle: %s", compiled)
	}
	for index, rule := range document.Routing.Rules[:2] {
		if len(rule.InboundTag) != 1 || rule.InboundTag[0] != document.Inbounds[index].Tag || rule.OutboundTag != document.Outbounds[index].Tag {
			t.Fatalf("route %d crossed rule boundaries: %s", index, compiled)
		}
	}
}

func TestCompileXrayBundleRejectsConflictingRoutingSettings(t *testing.T) {
	bundle := agentv1.ConfigurationBundle{SchemaVersion: BundleSchemaVersion, Fragments: []agentv1.ConfigurationFragment{
		{GroupID: "a", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[],"outbounds":[],"routing":{"domainStrategy":"AsIs","rules":[]}}`)},
		{GroupID: "b", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[],"outbounds":[],"routing":{"domainStrategy":"IPIfNonMatch","rules":[]}}`)},
	}}
	if _, err := CompileXrayBundle(bundle); !errors.Is(err, ErrXrayConfigConflict) {
		t.Fatalf("conflicting routing settings error = %v", err)
	}
}

func TestCompileXrayBundleAddsOneLoopbackStatsListener(t *testing.T) {
	bundle := agentv1.ConfigurationBundle{
		SchemaVersion: BundleSchemaVersion,
		Fragments: []agentv1.ConfigurationFragment{
			{GroupID: "rule-a", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"api":{"tag":"hl-stats-api","services":["StatsService"]},"stats":{},"inbounds":[{"tag":"in-a"}],"outbounds":[{"tag":"out-a"}],"routing":{"rules":[{"type":"field","inboundTag":["in-a"],"outboundTag":"out-a"}]}}`)},
			{GroupID: "rule-b", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"api":{"tag":"hl-stats-api","services":["StatsService"]},"stats":{},"inbounds":[{"tag":"in-b"}],"outbounds":[{"tag":"out-b"}],"routing":{"rules":[{"type":"field","inboundTag":["in-b"],"outboundTag":"out-b"}]}}`)},
		},
	}
	compiled, err := CompileXrayBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Listen   string `json:"listen"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(compiled, &document); err != nil {
		t.Fatal(err)
	}
	apiInbounds := 0
	apiOutbounds := 0
	for _, inbound := range document.Inbounds {
		if inbound.Tag == "hl-stats-api-in" {
			apiInbounds++
			if inbound.Listen != "127.0.0.1" || inbound.Port != 10085 || inbound.Protocol != "dokodemo-door" {
				t.Fatalf("stats listener = %#v", inbound)
			}
		}
	}
	for _, outbound := range document.Outbounds {
		if outbound.Tag == "hl-stats-api" {
			apiOutbounds++
		}
	}
	if apiInbounds != 1 || apiOutbounds != 1 {
		t.Fatalf("stats API entries = inbounds %d, outbounds %d; config %s", apiInbounds, apiOutbounds, compiled)
	}
	foundRoute := false
	for _, rule := range document.Routing.Rules {
		if len(rule.InboundTag) == 1 && rule.InboundTag[0] == "hl-stats-api-in" && rule.OutboundTag == "hl-stats-api" {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatalf("stats API routing rule missing: %s", compiled)
	}
	var policy struct {
		System struct {
			StatsInboundUplink    bool `json:"statsInboundUplink"`
			StatsInboundDownlink  bool `json:"statsInboundDownlink"`
			StatsOutboundUplink   bool `json:"statsOutboundUplink"`
			StatsOutboundDownlink bool `json:"statsOutboundDownlink"`
		} `json:"system"`
	}
	if err := json.Unmarshal(documentRaw(compiled, "policy"), &policy); err != nil {
		t.Fatal(err)
	}
	if !policy.System.StatsInboundUplink || !policy.System.StatsInboundDownlink || !policy.System.StatsOutboundUplink || !policy.System.StatsOutboundDownlink {
		t.Fatalf("stats policy is not enabled: %s", compiled)
	}
}

func TestCompileXrayBundleUpgradesLegacyFragmentsWithStats(t *testing.T) {
	bundle := agentv1.ConfigurationBundle{
		SchemaVersion: BundleSchemaVersion,
		Fragments: []agentv1.ConfigurationFragment{
			{GroupID: "legacy-rule", GroupRevision: 1, Engine: agentv1.EngineXray, Config: json.RawMessage(`{"inbounds":[{"tag":"legacy-in"}],"outbounds":[{"tag":"legacy-out","protocol":"freedom"}],"routing":{"rules":[{"type":"field","inboundTag":["legacy-in"],"outboundTag":"legacy-out"}]}}`)},
		},
	}
	compiled, err := CompileXrayBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		API      map[string]any `json:"api"`
		Stats    map[string]any `json:"stats"`
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
		Policy struct {
			System map[string]bool `json:"system"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(compiled, &document); err != nil {
		t.Fatal(err)
	}
	if document.API["tag"] != "hl-stats-api" || document.API["services"].([]any)[0] != "StatsService" {
		t.Fatalf("legacy fragment was not upgraded with StatsService: %s", compiled)
	}
	if document.Stats == nil || document.Policy.System["statsInboundUplink"] != true || document.Policy.System["statsInboundDownlink"] != true {
		t.Fatalf("legacy fragment was not upgraded with stats policy: %s", compiled)
	}
	found := false
	for _, inbound := range document.Inbounds {
		if inbound.Tag == "hl-stats-api-in" {
			found = true
		}
	}
	if !found {
		t.Fatalf("legacy fragment is missing stats listener: %s", compiled)
	}
}

func documentRaw(documentBytes []byte, key string) json.RawMessage {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(documentBytes, &document); err != nil {
		return nil
	}
	return document[key]
}

type recordingXrayRunner struct {
	mu          sync.Mutex
	tests       []string
	starts      []string
	processes   []*recordingXrayProcess
	testErr     error
	startErrors []error
}

func (r *recordingXrayRunner) Test(_ context.Context, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tests = append(r.tests, path)
	return r.testErr
}

func (r *recordingXrayRunner) Start(_ context.Context, path string) (XrayProcess, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, path)
	if len(r.startErrors) > 0 {
		err := r.startErrors[0]
		r.startErrors = r.startErrors[1:]
		return nil, err
	}
	process := &recordingXrayProcess{running: true}
	r.processes = append(r.processes, process)
	return process, nil
}

type recordingXrayProcess struct {
	mu        sync.Mutex
	running   bool
	stopCount int
}

func (p *recordingXrayProcess) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopCount++
	p.running = false
	return nil
}

func (p *recordingXrayProcess) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func newTestXrayAdapter(t *testing.T, runner XrayRunner, autoStart bool) *XrayProcessAdapter {
	t.Helper()
	root := t.TempDir()
	adapter, err := NewXrayProcessAdapter(XrayAdapterOptions{
		StagingDir: filepath.Join(root, "staging"),
		ActivePath: filepath.Join(root, "active", "config.json"),
		Runner:     runner,
		AutoStart:  autoStart,
	})
	if err != nil {
		t.Fatalf("NewXrayProcessAdapter() error = %v", err)
	}
	return adapter
}

func xrayDesired(t *testing.T, generation agentv1.NodeConfigGeneration, fragments []agentv1.ConfigurationFragment) agentv1.DesiredNodeConfig {
	t.Helper()
	raw, err := json.Marshal(agentv1.ConfigurationBundle{SchemaVersion: BundleSchemaVersion, Fragments: fragments})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	return agentv1.DesiredNodeConfig{Generation: generation, Engine: agentv1.EngineNodeBundle, ConfigSHA256: hex.EncodeToString(hash[:]), Config: raw}
}
