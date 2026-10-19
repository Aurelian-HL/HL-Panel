//go:build !linux

package hostprobe

import "github.com/hongle/hl-panel/internal/protocol/agentv1"

func SetTCPGeneration(agentv1.NodeConfigGeneration) {}
func InvalidateTCP()                                {}

func Snapshot() *agentv1.HostSnapshot { return nil }
