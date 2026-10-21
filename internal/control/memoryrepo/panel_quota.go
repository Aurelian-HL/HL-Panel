package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/panelquota"
)

// The provider reads only trusted local entitlements; it must not call the
// repository or the network. It is never serialized into a snapshot or import.
func (s *Store) SetPanelQuotaProvider(provider panelquota.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panelQuota = provider
}

func (s *Store) PanelResourceUsage(_ context.Context) (panelquota.Usage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.panelResourceUsageLocked(), nil
}

func (s *Store) panelResourceUsageLocked() panelquota.Usage {
	return panelquota.Usage{DeviceGroups: len(s.deviceGroups), Rules: len(s.forwardRules)}
}

func (s *Store) validatePanelGrowthLocked(after panelquota.Usage) error {
	if s.panelQuota == nil {
		return nil
	}
	return s.panelQuota().ValidateGrowth(s.panelResourceUsageLocked(), after)
}
