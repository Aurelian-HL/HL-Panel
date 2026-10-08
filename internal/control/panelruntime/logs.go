package panelruntime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxLogBytes = 2 << 20

type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type LogResult struct {
	Items      []LogEntry `json:"items"`
	Persistent bool       `json:"persistent"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// LogStore keeps this panel's own records. It does not grant access to the
// machine's journal or accept paths/commands supplied through an HTTP request.
type LogStore struct {
	mu         sync.Mutex
	path       string
	items      []LogEntry
	persistent bool
}

// MergeMigration restores bounded, sanitized panel logs without duplicating
// already-imported records on subsequent service restarts.
func (s *LogStore) MergeMigration(entries []LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[LogEntry]bool{}
	for _, entry := range s.items {
		seen[entry] = true
	}
	for _, entry := range entries {
		if clean, ok := normalizeLog(entry); ok && !seen[clean] {
			s.items = append(s.items, clean)
			seen[clean] = true
		}
	}
	sort.SliceStable(s.items, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, s.items[i].Time)
		b, _ := time.Parse(time.RFC3339Nano, s.items[j].Time)
		return a.Before(b)
	})
	s.persist()
}

var sensitiveLog = regexp.MustCompile(`(?i)(bearer\s+\S+|(?:enr_|ncr_|sub_)[A-Za-z0-9_-]+|(?:password|token|secret|credential|private[_ -]?key)\s*[:=]\s*[^\s,;]+|postgres(?:ql)?://\S+|https?://\S+[?]\S+)`)

func NewLogStore(path string) *LogStore {
	s := &LogStore{path: path, items: []LogEntry{}}
	if path == "" {
		return s
	}
	info, err := os.Lstat(path)
	if (err != nil && !os.IsNotExist(err)) || (err == nil && (!info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) || info.Size() > maxLogBytes)) {
		s.path = ""
		return s
	}
	if err == nil && info.Size() <= maxLogBytes {
		f, err := os.Open(path)
		if err == nil {
			scanner := bufio.NewScanner(io.LimitReader(f, maxLogBytes))
			scanner.Buffer(make([]byte, 4096), 65536)
			for scanner.Scan() {
				var e LogEntry
				if json.Unmarshal(scanner.Bytes(), &e) == nil {
					if e, ok := normalizeLog(e); ok {
						s.items = append(s.items, e)
					}
				}
			}
			f.Close()
		}
	}
	if len(s.items) > 2000 {
		s.items = s.items[len(s.items)-2000:]
	}
	s.persist()
	return s
}

func (s *LogStore) persist() {
	// JSON escaping can expand a message. Bound encoded bytes as well as count,
	// so every snapshot can be loaded after a restart.
	lines := make([][]byte, 0, len(s.items))
	size := 0
	for _, e := range s.items {
		line, _ := json.Marshal(e)
		line = append(line, '\n')
		lines = append(lines, line)
		size += len(line)
	}
	start := 0
	for len(lines)-start > 2000 || size > maxLogBytes {
		size -= len(lines[start])
		start++
	}
	s.items = append([]LogEntry{}, s.items[start:]...)
	if s.path == "" {
		return
	}
	// Write an atomic bounded snapshot; never follow an existing symlink.
	f, err := os.CreateTemp(filepath.Dir(s.path), ".panel-log-*")
	if err != nil {
		s.persistent = false
		return
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = io.Copy(f, bytes.NewReader(bytes.Join(lines[start:], nil)))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		err = os.Rename(name, s.path)
	}
	s.persistent = err == nil && closeErr == nil
}

func (s *LogStore) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var raw struct {
			Time    string `json:"time"`
			Level   string `json:"level"`
			Message string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		// Arbitrary logger attributes (errors, request bodies, config) are excluded.
		if entry, ok := normalizeLog(LogEntry{raw.Time, raw.Level, raw.Message}); ok {
			s.items = append(s.items, entry)
		}
	}
	if len(s.items) > 2000 {
		s.items = s.items[len(s.items)-2000:]
	}
	s.persist()
	return len(data), nil
}

func normalizeLog(e LogEntry) (LogEntry, bool) {
	if _, err := time.Parse(time.RFC3339Nano, e.Time); err != nil {
		return LogEntry{}, false
	}
	e.Level = strings.ToUpper(e.Level)
	if e.Level != "INFO" && e.Level != "WARN" && e.Level != "ERROR" {
		return LogEntry{}, false
	}
	e.Message = sensitiveLog.ReplaceAllString(e.Message, "[已隐藏]")
	if len(e.Message) > 2048 {
		e.Message = e.Message[:2048]
		for !utf8.ValidString(e.Message) {
			e.Message = e.Message[:len(e.Message)-1]
		}
	}
	return e, true
}

func (s *LogStore) Read(limit int) LogResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 1 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}
	start := len(s.items) - limit
	if start < 0 {
		start = 0
	}
	return LogResult{Items: append([]LogEntry{}, s.items[start:]...), Persistent: s.persistent, UpdatedAt: time.Now().UTC()}
}
