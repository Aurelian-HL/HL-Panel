package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/agent/config"
	usageagent "github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type unavailableCounter struct{}

func (unavailableCounter) CollectUsage(context.Context, usageagent.CollectionWindow) ([]usageagent.CounterDelta, error) {
	return nil, errors.New("test counter unavailable")
}

type unavailableExecutor struct{}

func (unavailableExecutor) ApplyEnforcement(context.Context, agentv1.EnforcementCommand) error {
	return errors.New("test executor unavailable")
}

func TestUsageOptionsFailClosedWithoutAgentOwnedXrayAndBothAdapters(t *testing.T) {
	paired := Options{UsageSource: unavailableCounter{}, EnforcementExecutor: unavailableExecutor{}}
	tests := []struct {
		name    string
		cfg     config.Config
		options Options
		valid   bool
	}{
		{name: "default has no accounting", cfg: config.Config{}, valid: true},
		{name: "source only", cfg: config.Config{EngineMode: config.EngineModeXray, XrayAutoStart: true}, options: Options{UsageSource: unavailableCounter{}}, valid: true},
		{name: "executor only", cfg: config.Config{EngineMode: config.EngineModeXray, XrayAutoStart: true}, options: Options{EnforcementExecutor: unavailableExecutor{}}, valid: true},
		{name: "dry run", cfg: config.Config{EngineMode: config.EngineModeDryRun}, options: paired},
		{name: "gost", cfg: config.Config{EngineMode: config.EngineModeGOST}, options: paired},
		{name: "external xray accounting", cfg: config.Config{EngineMode: config.EngineModeXray}, options: Options{UsageSource: unavailableCounter{}}, valid: true},
		{name: "external xray enforcement", cfg: config.Config{EngineMode: config.EngineModeXray}, options: Options{EnforcementExecutor: unavailableExecutor{}}, valid: false},
		{name: "owned xray integration hook", cfg: config.Config{EngineMode: config.EngineModeXray, XrayAutoStart: true}, options: paired, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateUsageOptions(test.cfg, test.options)
			if (err == nil) != test.valid {
				t.Fatalf("validateUsageOptions() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestUsageEngineMustBeRunning(t *testing.T) {
	if err := (&Agent{}).requireOwnedXray(); err == nil {
		t.Fatal("missing Xray process was accepted as a usage engine")
	}
}
