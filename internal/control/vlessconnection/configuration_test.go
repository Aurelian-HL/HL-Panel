package vlessconnection

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
)

type configurationTestSource struct {
	calls       int
	admin, rule string
	result      Configuration
	err         error
}

func (*configurationTestSource) ResolveSubscriptionLines(context.Context, string, string, []string) (map[string]string, map[string]error, error) {
	panic("configuration must not fall back to subscription reads")
}

func (s *configurationTestSource) ResolveForwardingConfiguration(_ context.Context, admin, rule string) (Configuration, error) {
	s.calls++
	s.admin, s.rule = admin, rule
	return s.result, s.err
}

type configurationBatchOnly struct{}

func (configurationBatchOnly) ResolveSubscriptionLines(context.Context, string, string, []string) (map[string]string, map[string]error, error) {
	panic("separate subscription reads are forbidden")
}

func TestConfigurationServiceRequiresAuthoritativeSource(t *testing.T) {
	for _, service := range []*Service{nil, NewService(nil, nil, nil), NewService(nil, nil, nil, configurationBatchOnly{})} {
		got, err := service.ResolveForwardingConfiguration(context.Background(), "admin", "rule")
		if !errors.Is(err, faults.ErrConflict) || got != (Configuration{}) {
			t.Fatal("missing authoritative source did not fail closed")
		}
	}
	source := &configurationTestSource{result: Configuration{RuleID: "rule", Status: "paused", Paused: true}}
	service := NewService(nil, nil, nil, source)
	got, err := service.ResolveForwardingConfiguration(context.Background(), " admin ", " rule ")
	if err != nil || got != source.result || source.calls != 1 || source.admin != "admin" || source.rule != "rule" {
		t.Fatal("configuration did not use a single authorized source")
	}
	for _, ids := range [][2]string{{"", "rule"}, {"admin", " "}} {
		if got, err := service.ResolveForwardingConfiguration(context.Background(), ids[0], ids[1]); !errors.Is(err, faults.ErrValidation) || got != (Configuration{}) {
			t.Fatal("invalid identity reached the source")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := service.ResolveForwardingConfiguration(ctx, "admin", "rule"); !errors.Is(err, context.Canceled) || got != (Configuration{}) || source.calls != 1 {
		t.Fatal("canceled read reached the source")
	}
}
