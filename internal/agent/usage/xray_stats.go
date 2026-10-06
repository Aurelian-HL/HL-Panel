package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/agent/usage/xraystatsproto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// XrayStatsClient is the small part of Xray's StatsService used by the
// accounting source. Keeping this interface makes counter/reset behavior
// testable without starting an Xray process.
type XrayStatsClient interface {
	QueryStats(context.Context, *xraystatsproto.QueryStatsRequest, ...grpc.CallOption) (*xraystatsproto.QueryStatsResponse, error)
}

type xrayStatMetadata struct {
	RuleID     string `json:"rule_id"`
	CustomerID string `json:"customer_id"`
	EntryGroup string `json:"entry_group_id"`
	ExitGroup  string `json:"exit_group_id"`
	Protocol   string `json:"protocol"`
	Legacy     bool   `json:"-"`
}

type xrayCounter struct {
	metadata xrayStatMetadata
	inUp     int64
	inDown   int64
	outUp    int64
	outDown  int64
	hasIn    bool
	hasOut   bool
}

// XrayStatsSource reads cumulative Xray counters and emits non-negative
// deltas. A process restart or StatsService reset establishes a new baseline;
// it never creates a negative usage report.
type XrayStatsSource struct {
	client XrayStatsClient
	conn   *grpc.ClientConn
	mu     sync.Mutex
	last   map[string]int64
	// initialized is separate from last being non-empty. Xray legitimately
	// returns an empty stats list while no rule has traffic yet; that must still
	// establish the first sample and must not make a later sample look like a
	// second baseline. Keeping this bit also lets us preserve the last known
	// counters across a transient empty response.
	initialized bool
	checkpoint  *xrayStatsCheckpoint
}

// xrayStatsReadyTimeout bounds the readiness wait for a supervised Xray
// process. The edge agent and Xray can be started by separate systemd units;
// waiting for the channel to become ready avoids a spurious first
// "connection refused" report while still allowing the periodic reporter to
// retry when Xray takes longer to start or is unavailable.
const xrayStatsReadyTimeout = 5 * time.Second

type xrayStatsCheckpoint struct {
	last        map[string]int64
	initialized bool
}

func NewXrayStatsSource(address string) (*XrayStatsSource, error) {
	if err := validateStatsAddress(address); err != nil {
		return nil, err
	}
	conn, err := grpc.Dial(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect Xray StatsService: %w", err)
	}
	return NewXrayStatsSourceWithClient(xraystatsproto.NewStatsServiceClient(conn), conn), nil
}

func NewXrayStatsSourceWithClient(client XrayStatsClient, conn *grpc.ClientConn) *XrayStatsSource {
	return &XrayStatsSource{client: client, conn: conn, last: make(map[string]int64)}
}

func (source *XrayStatsSource) Close() error {
	if source == nil || source.conn == nil {
		return nil
	}
	return source.conn.Close()
}

// BeginCollection snapshots the source cursor before a reporter collection.
// The snapshot protects the failure path where validation or journal
// persistence fails after a stats query.
func (source *XrayStatsSource) BeginCollection() error {
	if source == nil {
		return ErrCounterSourceUnavailable
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.checkpoint != nil {
		return errors.New("xray usage collection checkpoint is already active")
	}
	source.checkpoint = &xrayStatsCheckpoint{last: cloneStatsCursor(source.last), initialized: source.initialized}
	return nil
}

func (source *XrayStatsSource) CommitCollection() {
	if source == nil {
		return
	}
	source.mu.Lock()
	source.checkpoint = nil
	source.mu.Unlock()
}

func (source *XrayStatsSource) RollbackCollection() {
	if source == nil {
		return
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.checkpoint == nil {
		return
	}
	source.last = source.checkpoint.last
	source.initialized = source.checkpoint.initialized
	source.checkpoint = nil
}

func cloneStatsCursor(source map[string]int64) map[string]int64 {
	copyOf := make(map[string]int64, len(source))
	for key, value := range source {
		copyOf[key] = value
	}
	return copyOf
}

func (source *XrayStatsSource) CollectUsage(ctx context.Context, window CollectionWindow) ([]CounterDelta, error) {
	if source == nil || source.client == nil {
		return nil, ErrCounterSourceUnavailable
	}
	if window.EndedAt.IsZero() || !window.EndedAt.After(window.StartedAt) {
		return nil, errors.New("usage collection window is invalid")
	}
	queryContext, cancel := context.WithTimeout(ctx, xrayStatsReadyTimeout)
	defer cancel()
	response, err := source.client.QueryStats(queryContext, &xraystatsproto.QueryStatsRequest{Pattern: "", Reset_: false}, grpc.WaitForReady(true))
	if err != nil {
		return nil, fmt.Errorf("query Xray traffic stats: %w", err)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	current := make(map[string]int64, len(response.GetStat()))
	for _, stat := range response.GetStat() {
		if stat == nil || stat.GetName() == "" || stat.GetValue() < 0 {
			continue
		}
		current[stat.GetName()] = stat.GetValue()
	}
	if !source.initialized {
		source.last = current
		source.initialized = true
		return nil, nil
	}
	// An empty response is not evidence that all counters reset. StatsService
	// can briefly return no rows while Xray reloads a configuration or has no
	// active stats. Preserve the last sample so the next valid response is
	// differenced against the real baseline instead of being silently dropped.
	if len(current) == 0 {
		return nil, nil
	}
	deltas := make(map[string]int64, len(current))
	for name, value := range current {
		previous := source.last[name]
		if value < previous {
			// A decreased counter indicates a reset. New counters after the
			// initial sample are lazy stats and must include their first bytes.
			deltas[name] = 0
			continue
		}
		deltas[name] = value - previous
	}
	for name, value := range current {
		source.last[name] = value
	}
	return buildXrayDeltas(current, deltas, window.EndedAt), nil
}

func buildXrayDeltas(_ map[string]int64, increments map[string]int64, occurredAt time.Time) []CounterDelta {
	byKey := make(map[string]*xrayCounter)
	for name, increment := range increments {
		if increment <= 0 {
			continue
		}
		kind, tag, direction, ok := parseXrayTrafficStat(name)
		if !ok {
			continue
		}
		metadata, ok := decodeStatsTag(tag)
		if !ok {
			continue
		}
		key := metadata.RuleID + "\x00" + metadata.CustomerID + "\x00" + metadata.EntryGroup + "\x00" + metadata.ExitGroup + "\x00" + metadata.Protocol
		if metadata.Legacy {
			key = "legacy\x00" + metadata.RuleID
		}
		counter := byKey[key]
		if counter == nil {
			counter = &xrayCounter{metadata: metadata}
			byKey[key] = counter
		}
		switch {
		case kind == "inbound" && direction == "uplink":
			counter.inUp += increment
			counter.hasIn = true
		case kind == "inbound" && direction == "downlink":
			counter.inDown += increment
			counter.hasIn = true
		case kind == "outbound" && direction == "uplink":
			counter.outUp += increment
			counter.hasOut = true
		case kind == "outbound" && direction == "downlink":
			counter.outDown += increment
			counter.hasOut = true
		}
	}
	result := make([]CounterDelta, 0, len(byKey))
	for _, counter := range byKey {
		actual := counter.inUp + counter.inDown
		if !counter.hasIn {
			actual = counter.outUp + counter.outDown
		}
		if actual <= 0 || !validIdentifier(counter.metadata.RuleID) {
			continue
		}
		if counter.metadata.Legacy {
			result = append(result, CounterDelta{
				RuleID: counter.metadata.RuleID, LegacyRuleID: counter.metadata.RuleID,
				OccurredAt: occurredAt, RuleActualBytes: actual, CustomerActualBytes: actual,
				EntryMultiplierMicros: 1_000_000, ExitMultiplierMicros: 1_000_000,
			})
			continue
		}
		if !validIdentifier(counter.metadata.CustomerID) || !validIdentifier(counter.metadata.EntryGroup) {
			continue
		}
		protocol := strings.ToLower(strings.TrimSpace(counter.metadata.Protocol))
		if protocol != string("tcp") && protocol != string("udp") {
			continue
		}
		result = append(result, CounterDelta{
			CustomerID: counter.metadata.CustomerID, RuleID: counter.metadata.RuleID,
			EntryGroupID: counter.metadata.EntryGroup, ExitGroupID: counter.metadata.ExitGroup,
			Protocol: protocol, OccurredAt: occurredAt, RuleActualBytes: actual,
			CustomerActualBytes: actual, EntryMultiplierMicros: 1_000_000,
			ExitMultiplierMicros: 1_000_000,
		})
	}
	return result
}

func parseXrayTrafficStat(name string) (kind, tag, direction string, ok bool) {
	parts := strings.Split(name, ">>>")
	if len(parts) != 4 || parts[2] != "traffic" || (parts[0] != "inbound" && parts[0] != "outbound") || (parts[3] != "uplink" && parts[3] != "downlink") {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[3], true
}

func decodeStatsTag(tag string) (xrayStatMetadata, bool) {
	const legacyPrefix = "vless-reality-fwd_"
	if ruleID, ok := strings.CutPrefix(tag, legacyPrefix); ok && validIdentifier(ruleID) && len(ruleID) <= 128 {
		return xrayStatMetadata{RuleID: ruleID, Legacy: true}, true
	}
	separator := strings.LastIndex(tag, "--")
	if separator < 1 {
		return xrayStatMetadata{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(tag[separator+2:])
	if err != nil {
		return xrayStatMetadata{}, false
	}
	var metadata xrayStatMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata.RuleID == "" {
		return xrayStatMetadata{}, false
	}
	return metadata, true
}

func validateStatsAddress(address string) error {
	address = strings.TrimSpace(address)
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("xray_stats_api_address must be host:port")
	}
	if host == "localhost" {
		return nil
	}
	parsed, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !parsed.IsLoopback() {
		return errors.New("xray_stats_api_address must bind to loopback")
	}
	return nil
}

// ResetCounters is called for a known new process, whose counters start at
// zero. This includes first traffic before the first periodic sample.
func (s *XrayStatsSource) ResetCounters() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = make(map[string]int64)
	s.initialized = true
}
