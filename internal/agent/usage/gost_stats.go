package usage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	gostInputMetric  = "gost_service_transfer_input_bytes_total"
	gostOutputMetric = "gost_service_transfer_output_bytes_total"
)

// GOSTStatsSource reads authoritative per-service cumulative counters from
// GOST's loopback-only Prometheus endpoint. Services are named
// forward-<rule-id>; reports intentionally carry only the legacy rule ID.
type GOSTStatsSource struct {
	client      *http.Client
	url         string
	mu          sync.Mutex
	last        map[string]int64
	initialized bool
	checkpoint  *gostStatsCheckpoint
}

type gostStatsCheckpoint struct {
	last        map[string]int64
	initialized bool
}

func NewGOSTStatsSource(address string) (*GOSTStatsSource, error) {
	address = strings.TrimSpace(address)
	if err := validateStatsAddress(address); err != nil {
		return nil, fmt.Errorf("gost metrics address: %w", err)
	}
	return NewGOSTStatsSourceWithClient(address, &http.Client{Timeout: 10 * time.Second}), nil
}

func NewGOSTStatsSourceWithClient(address string, client *http.Client) *GOSTStatsSource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &GOSTStatsSource{client: client, url: "http://" + strings.TrimSpace(address) + "/metrics", last: make(map[string]int64)}
}

func (source *GOSTStatsSource) Close() error { return nil }

func (source *GOSTStatsSource) BeginCollection() error {
	if source == nil {
		return ErrCounterSourceUnavailable
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.checkpoint != nil {
		return errors.New("gost usage collection checkpoint is already active")
	}
	source.checkpoint = &gostStatsCheckpoint{last: cloneGOSTCursor(source.last), initialized: source.initialized}
	return nil
}

func (source *GOSTStatsSource) CommitCollection() {
	if source == nil {
		return
	}
	source.mu.Lock()
	source.checkpoint = nil
	source.mu.Unlock()
}

func (source *GOSTStatsSource) RollbackCollection() {
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

func cloneGOSTCursor(value map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func (source *GOSTStatsSource) CollectUsage(ctx context.Context, window CollectionWindow) ([]CounterDelta, error) {
	if source == nil || source.client == nil {
		return nil, ErrCounterSourceUnavailable
	}
	if window.EndedAt.IsZero() || !window.EndedAt.After(window.StartedAt) {
		return nil, errors.New("usage collection window is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.url, nil)
	if err != nil {
		return nil, fmt.Errorf("create GOST metrics request: %w", err)
	}
	response, err := source.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query GOST traffic metrics: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, fmt.Errorf("query GOST traffic metrics: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	current, err := parseGOSTMetrics(response.Body)
	if err != nil {
		return nil, fmt.Errorf("decode GOST traffic metrics: %w", err)
	}

	source.mu.Lock()
	defer source.mu.Unlock()
	if !source.initialized {
		source.last = current
		source.initialized = true
		return nil, nil
	}
	if len(current) == 0 {
		return nil, nil
	}
	deltas := make([]CounterDelta, 0, len(current))
	for ruleID, value := range current {
		previous := source.last[ruleID]
		// GOST creates service counters lazily on the first connection. After
		// a successful initial sample, a newly appearing counter starts at zero.
		if value < previous {
			continue
		}
		if increment := value - previous; increment > 0 {
			deltas = append(deltas, CounterDelta{RuleID: ruleID, LegacyRuleID: ruleID, OccurredAt: window.EndedAt, RuleActualBytes: increment, CustomerActualBytes: increment, EntryMultiplierMicros: 1_000_000, ExitMultiplierMicros: 1_000_000})
		}
	}
	for ruleID, value := range current {
		source.last[ruleID] = value
	}
	return deltas, nil
}

// parseGOSTMetrics parses only the two per-service Prometheus counters. Host
// totals and unrelated metrics are ignored, so they cannot become rule usage.
func parseGOSTMetrics(reader io.Reader) (map[string]int64, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 8<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	input := make(map[string]int64)
	output := make(map[string]int64)
	seenContent := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seenContent = true
		name, labels, value, targeted, err := parseGOSTSample(line)
		if err != nil {
			return nil, err
		}
		if !targeted {
			continue
		}
		service := strings.TrimSpace(labels["service"])
		if service == "" {
			// Aggregate GOST counters may omit the service label. They cannot
			// be attributed to an HL rule, so ignore them like other totals.
			continue
		}
		ruleID, ok := strings.CutPrefix(service, "forward-")
		if !ok {
			// A GOST process may expose counters for services managed outside
			// HL-panel. They are not customer rules and must not poison the
			// complete collection window.
			continue
		}
		if !validIdentifier(ruleID) {
			return nil, fmt.Errorf("metric %q has invalid service label", name)
		}
		target := input
		if name == gostOutputMetric {
			target = output
		}
		updated, err := addGOSTBytes(target[ruleID], value)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", service, err)
		}
		target[ruleID] = updated
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read metrics body: %w", err)
	}
	if !seenContent {
		return nil, errors.New("metrics body is empty")
	}
	current := make(map[string]int64, len(input)+len(output))
	for ruleID, value := range input {
		current[ruleID] = value
	}
	for ruleID, value := range output {
		updated, err := addGOSTBytes(current[ruleID], value)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", ruleID, err)
		}
		current[ruleID] = updated
	}
	return current, nil
}

func addGOSTBytes(current, value int64) (int64, error) {
	if value < 0 || current < 0 || current > math.MaxInt64-value {
		return 0, errors.New("byte counter is negative or overflows")
	}
	return current + value, nil
}

func parseGOSTSample(line string) (name string, labels map[string]string, value int64, targeted bool, err error) {
	labels = make(map[string]string)
	brace := strings.IndexByte(line, '{')
	space := strings.IndexAny(line, " \t")
	if brace >= 0 && (space < 0 || brace < space) {
		close := strings.LastIndexByte(line, '}')
		if close < brace {
			return "", nil, 0, false, errors.New("malformed metric labels")
		}
		name = strings.TrimSpace(line[:brace])
		labels, err = parseGOSTLabels(line[brace+1 : close])
		if err != nil {
			return "", nil, 0, false, err
		}
		fields := strings.Fields(strings.TrimSpace(line[close+1:]))
		if len(fields) == 0 {
			return "", nil, 0, false, errors.New("metric sample has no value")
		}
		if name != gostInputMetric && name != gostOutputMetric {
			return name, labels, 0, false, nil
		}
		value, err = parseGOSTValue(fields[0])
		return name, labels, value, true, err
	}
	if space < 0 {
		return "", nil, 0, false, errors.New("metric sample has no value")
	}
	name = strings.TrimSpace(line[:space])
	if name != gostInputMetric && name != gostOutputMetric {
		return name, labels, 0, false, nil
	}
	fields := strings.Fields(strings.TrimSpace(line[space:]))
	if len(fields) == 0 {
		return "", nil, 0, true, errors.New("targeted metric sample has no value")
	}
	value, err = parseGOSTValue(fields[0])
	return name, labels, value, true, err
}

func parseGOSTValue(raw string) (int64, error) {
	// Parse through an exact rational representation. Using float64 here would
	// silently round large counters (for example 9007199254740993).
	parsed, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok || parsed.Sign() < 0 || !parsed.IsInt() || parsed.Num().Cmp(big.NewInt(math.MaxInt64)) > 0 {
		return 0, errors.New("targeted metric value is not a non-negative integer")
	}
	return parsed.Num().Int64(), nil
}

func parseGOSTLabels(raw string) (map[string]string, error) {
	labels := make(map[string]string)
	for {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return labels, nil
		}
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			return nil, errors.New("malformed metric label")
		}
		key := strings.TrimSpace(raw[:eq])
		if !validMetricLabelName(key) {
			return nil, errors.New("invalid metric label name")
		}
		raw = strings.TrimSpace(raw[eq+1:])
		if len(raw) == 0 || raw[0] != '"' {
			return nil, errors.New("metric label value must be quoted")
		}
		var value strings.Builder
		closed := false
		for i := 1; i < len(raw); i++ {
			switch raw[i] {
			case '"':
				raw = raw[i+1:]
				closed = true
			case '\\':
				if i+1 >= len(raw) {
					return nil, errors.New("unterminated metric label escape")
				}
				i++
				switch raw[i] {
				case 'n':
					value.WriteByte('\n')
				case 't':
					value.WriteByte('\t')
				case 'r':
					value.WriteByte('\r')
				case '\\', '"':
					value.WriteByte(raw[i])
				default:
					return nil, errors.New("invalid metric label escape")
				}
			default:
				value.WriteByte(raw[i])
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, errors.New("unterminated metric label")
		}
		if _, exists := labels[key]; exists {
			return nil, errors.New("duplicate metric label")
		}
		labels[key] = value.String()
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return labels, nil
		}
		if raw[0] != ',' {
			return nil, errors.New("metric labels must be comma separated")
		}
		raw = raw[1:]
	}
}

func validMetricLabelName(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

// ResetCounters is called for a known new process, whose counters start at
// zero. This includes first traffic before the first periodic sample.
func (s *GOSTStatsSource) ResetCounters() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = make(map[string]int64)
	s.initialized = true
}
