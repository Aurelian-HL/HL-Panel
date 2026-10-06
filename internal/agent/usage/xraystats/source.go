// Package xraystats reads per-inbound counters from the local Xray Stats API.
// It deliberately uses Xray's cumulative counters and computes deltas locally;
// host-wide network counters cannot be attributed to forwarding rules.
package xraystats

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/agent/usage/xraystatsproto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	statsPattern = "inbound>>>vless-reality-"
	tagPrefix    = "vless-reality-"
)

type metadata struct {
	RuleID     string `json:"rule_id"`
	CustomerID string `json:"customer_id"`
	EntryGroup string `json:"entry_group_id"`
	ExitGroup  string `json:"exit_group_id"`
	Protocol   string `json:"protocol"`
}

type counters struct {
	uplink   int64
	downlink int64
}

// Source is a process-local cumulative-counter differencer. Xray itself owns
// the counters; this object only remembers the previous successful sample.
type Source struct {
	conn     *grpc.ClientConn
	client   xraystatsproto.StatsServiceClient
	mu       sync.Mutex
	previous map[string]counters
}

func New(address string) (*Source, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, errors.New("xray stats API address is empty")
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect to xray stats API: %w", err)
	}
	return newWithClient(conn, xraystatsproto.NewStatsServiceClient(conn)), nil
}

func newWithClient(conn *grpc.ClientConn, client xraystatsproto.StatsServiceClient) *Source {
	return &Source{conn: conn, client: client, previous: make(map[string]counters)}
}

func (source *Source) Close() error {
	if source == nil || source.conn == nil {
		return nil
	}
	return source.conn.Close()
}

func (source *Source) CollectUsage(ctx context.Context, window usage.CollectionWindow) ([]usage.CounterDelta, error) {
	if source == nil || source.client == nil {
		return nil, errors.New("xray stats API client is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !window.EndedAt.After(window.StartedAt) {
		return nil, errors.New("usage collection window is invalid")
	}
	response, err := source.client.QueryStats(ctx, &xraystatsproto.QueryStatsRequest{Pattern: statsPattern, Reset_: false})
	if err != nil {
		return nil, fmt.Errorf("query xray inbound statistics: %w", err)
	}
	current, err := aggregate(response.GetStat())
	if err != nil {
		return nil, err
	}

	source.mu.Lock()
	defer source.mu.Unlock()
	deltas := make([]usage.CounterDelta, 0, len(current))
	// Stage cursor advancement until every non-zero delta has passed metadata
	// validation. A malformed tag must not make a later retry start from an
	// already-consumed counter value.
	nextPrevious := make(map[string]counters, len(source.previous)+len(current))
	for tag, value := range source.previous {
		nextPrevious[tag] = value
	}
	for tag, value := range current {
		old := source.previous[tag]
		uplink := counterDelta(value.uplink, old.uplink)
		downlink := counterDelta(value.downlink, old.downlink)
		nextPrevious[tag] = value
		if uplink == 0 && downlink == 0 {
			continue
		}
		meta := tagMetadata(tag)
		if err := validateMetadata(meta); err != nil {
			return nil, fmt.Errorf("decode xray inbound tag %q: %w", tag, err)
		}
		deltas = append(deltas, usage.CounterDelta{
			CustomerID: meta.CustomerID, RuleID: meta.RuleID, EntryGroupID: meta.EntryGroup,
			ExitGroupID: meta.ExitGroup, Protocol: strings.ToLower(meta.Protocol),
			OccurredAt: window.EndedAt, RuleActualBytes: uplink + downlink,
			CustomerActualBytes: uplink + downlink, EntryMultiplierMicros: 1_000_000,
			ExitMultiplierMicros: 1_000_000,
		})
	}
	source.previous = nextPrevious
	return deltas, nil
}

func counterDelta(current, previous int64) int64 {
	if current < 0 {
		return 0
	}
	if previous < 0 || current < previous {
		return current
	}
	return current - previous
}

func aggregate(stats []*xraystatsproto.Stat) (map[string]counters, error) {
	result := make(map[string]counters)
	for _, stat := range stats {
		if stat == nil || !strings.HasPrefix(stat.GetName(), statsPattern) {
			continue
		}
		name := stat.GetName()
		remainder := strings.TrimPrefix(name, "inbound>>>")
		parts := strings.Split(remainder, ">>>traffic>>>")
		if len(parts) != 2 || (parts[1] != "uplink" && parts[1] != "downlink") {
			continue
		}
		entry := result[parts[0]]
		if parts[1] == "uplink" {
			entry.uplink = stat.GetValue()
		} else {
			entry.downlink = stat.GetValue()
		}
		result[parts[0]] = entry
	}
	return result, nil
}

func tagMetadata(tag string) metadata {
	parts := strings.SplitN(tag, "--", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], tagPrefix) {
		return metadata{}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return metadata{}
	}
	var value metadata
	if json.Unmarshal(decoded, &value) != nil {
		return metadata{}
	}
	return value
}

func validateMetadata(value metadata) error {
	if strings.TrimSpace(value.RuleID) == "" || strings.TrimSpace(value.CustomerID) == "" || strings.TrimSpace(value.EntryGroup) == "" {
		return errors.New("rule, customer, and entry-group identifiers are required")
	}
	protocol := strings.ToLower(strings.TrimSpace(value.Protocol))
	if protocol != "tcp" && protocol != "udp" {
		return errors.New("unsupported protocol")
	}
	return nil
}
