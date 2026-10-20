// Package panelquota enforces panel-wide entitlements independently of owner quotas.
package panelquota

import (
	"context"
	"errors"
	"fmt"
)

var ErrExceeded = errors.New("panel resource quota exceeded")

type Usage struct {
	DeviceGroups int `json:"device_groups"`
	Rules        int `json:"rules"`
}

type UsageSource interface {
	PanelResourceUsage(context.Context) (Usage, error)
}

// Negative maxima permit unlimited growth. Zero permits maintenance only.
// A nil provider is reserved for installations without signed licensing.
type Limits struct {
	DeviceGroups int
	Rules        int
}

type Provider func() Limits

func (limits Limits) ValidateGrowth(before, after Usage) error {
	if limits.DeviceGroups >= 0 && after.DeviceGroups > before.DeviceGroups && after.DeviceGroups > limits.DeviceGroups {
		return fmt.Errorf("%w: 当前授权每个面板最多 %d 个设备组，已有 %d 个；请升级授权后再新增，现有设备组继续保留", ErrExceeded, limits.DeviceGroups, before.DeviceGroups)
	}
	if limits.Rules >= 0 && after.Rules > before.Rules && after.Rules > limits.Rules {
		return fmt.Errorf("%w: 当前授权每个面板最多 %d 条规则，已有 %d 条（含停用）；请升级授权后再新增，现有规则继续运行", ErrExceeded, limits.Rules, before.Rules)
	}
	return nil
}
