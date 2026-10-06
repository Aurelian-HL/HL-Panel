//go:build !linux

package hostprobe

import "github.com/hongle/hl-panel/internal/protocol/agentv1"

func Snapshot() *agentv1.HostSnapshot { return nil }
