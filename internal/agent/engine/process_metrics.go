package engine

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// RuntimeMetrics is a safe operational summary of an agent-owned engine
// process. It deliberately excludes stdout/stderr, which may contain target
// addresses or credentials from a user configuration.
type RuntimeMetrics struct {
	MemoryBytes   *uint64
	ThreadCount   *uint64
	UptimeSeconds *uint64
	PID           int
}

type runtimeMetricsProvider interface {
	RuntimeMetrics() RuntimeMetrics
}

func formatMetricBytes(value *uint64) string {
	if value == nil {
		return "未采集"
	}
	return fmt.Sprintf("%d B", *value)
}

func formatMetricCount(value *uint64) string {
	if value == nil {
		return "未采集"
	}
	return strconv.FormatUint(*value, 10)
}

func formatMetricUptime(value *uint64) string {
	if value == nil {
		return "未采集"
	}
	return fmt.Sprintf("%ds", *value)
}

func readProcessMetrics(pid int, startedAt time.Time) RuntimeMetrics {
	metrics := RuntimeMetrics{PID: pid}
	if pid <= 0 {
		return metrics
	}
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
			var memory, threads uint64
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				switch fields[0] {
				case "VmRSS:":
					if value, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						memory = value * 1024
					}
				case "Threads:":
					if value, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						threads = value
					}
				}
			}
			if memory > 0 {
				metrics.MemoryBytes = &memory
			}
			if threads > 0 {
				metrics.ThreadCount = &threads
			}
		}
	}
	if !startedAt.IsZero() {
		seconds := uint64(time.Since(startedAt).Seconds())
		metrics.UptimeSeconds = &seconds
	}
	return metrics
}

func mergeRuntimeMetrics(values ...RuntimeMetrics) RuntimeMetrics {
	var result RuntimeMetrics
	var memory, threads uint64
	var uptime uint64
	for _, value := range values {
		if value.MemoryBytes != nil {
			memory += *value.MemoryBytes
		}
		if value.ThreadCount != nil {
			threads += *value.ThreadCount
		}
		if value.UptimeSeconds != nil && *value.UptimeSeconds > uptime {
			uptime = *value.UptimeSeconds
		}
		if value.PID > 0 && result.PID == 0 {
			result.PID = value.PID
		}
	}
	if memory > 0 {
		result.MemoryBytes = &memory
	}
	if threads > 0 {
		result.ThreadCount = &threads
	}
	if uptime > 0 {
		result.UptimeSeconds = &uptime
	}
	return result
}
