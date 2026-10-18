package agentv1

// HostSnapshot contains host-wide measurements, separate from Go process memory.
// Nil measurements mean unavailable, never a fabricated zero.
type HostSnapshot struct {
	CPUPercent                *float64 `json:"cpu_percent,omitempty"`
	MemoryUsedBytes           *uint64  `json:"memory_used_bytes,omitempty"`
	MemoryTotalBytes          *uint64  `json:"memory_total_bytes,omitempty"`
	SwapUsedBytes             *uint64  `json:"swap_used_bytes,omitempty"`
	SwapTotalBytes            *uint64  `json:"swap_total_bytes,omitempty"`
	DiskUsedBytes             *uint64  `json:"disk_used_bytes,omitempty"`
	DiskTotalBytes            *uint64  `json:"disk_total_bytes,omitempty"`
	LoadAverage1              *float64 `json:"load_average_1,omitempty"`
	LoadAverage5              *float64 `json:"load_average_5,omitempty"`
	LoadAverage15             *float64 `json:"load_average_15,omitempty"`
	UptimeSeconds             *uint64  `json:"uptime_seconds,omitempty"`
	NetInTransferBytes        *uint64  `json:"net_in_transfer_bytes,omitempty"`
	NetOutTransferBytes       *uint64  `json:"net_out_transfer_bytes,omitempty"`
	NetInSpeedBytesPerSecond  *uint64  `json:"net_in_speed_bytes_per_second,omitempty"`
	NetOutSpeedBytesPerSecond *uint64  `json:"net_out_speed_bytes_per_second,omitempty"`
	TCPConnCount              *uint64  `json:"tcp_conn_count,omitempty"`
	// TCP congestion-control values are read from the kernel's effective
	// defaults. They describe what new sockets may inherit, not existing
	// connections or user-space QUIC/KCP congestion control.
	TCPCongestionControl          string   `json:"tcp_congestion_control,omitempty"`
	TCPAvailableCongestionControl []string `json:"tcp_available_congestion_control,omitempty"`
	// Local TCP listener port -> established connections. Null means unknown;
	// an empty object is a successful sample with no listeners. No peer IPs.
	TCPListeners       map[string]uint64 `json:"tcp_listeners"`
	UDPConnCount       *uint64           `json:"udp_conn_count,omitempty"`
	IPv4               string            `json:"ipv4,omitempty"`
	IPv6               string            `json:"ipv6,omitempty"`
	IPCollectionStatus string            `json:"ip_collection_status,omitempty"`
}
