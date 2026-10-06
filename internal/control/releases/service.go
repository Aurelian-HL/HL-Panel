// Package releases checks the project's published GitHub releases. It never
// sends administrator credentials, instance URLs or private state to GitHub.
package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const RepositoryURL = "https://github.com/Aurelian-HL/HL-Panel"
const apiURL = "https://api.github.com/repos/Aurelian-HL/HL-Panel/releases?per_page=100"

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type Status struct {
	Current       string    `json:"current_version"`
	Latest        string    `json:"latest_version"`
	Behind        int       `json:"versions_behind"`
	BehindAtLeast bool      `json:"versions_behind_at_least"`
	State         string    `json:"state"`
	Attention     bool      `json:"attention_required"`
	CanDefer      bool      `json:"can_defer"`
	CheckedAt     time.Time `json:"checked_at"`
	RepositoryURL string    `json:"repository_url"`
	ReleaseURL    string    `json:"release_url"`
	UpdateCommand string    `json:"update_command"`
	Message       string    `json:"message"`
}

type release struct {
	Tag         string     `json:"tag_name"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"`
}

type Service struct {
	current  string
	client   *http.Client
	endpoint string
	now      func() time.Time
	mu       sync.Mutex
	cached   Status
	expires  time.Time
}

func New(current string) *Service {
	return &Service{current: current, client: &http.Client{
		Timeout:       8 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, endpoint: apiURL, now: time.Now}
}

func parseVersion(tag string) ([3]uint64, bool) {
	var version [3]uint64
	parts := versionPattern.FindStringSubmatch(tag)
	if parts == nil {
		return version, false
	}
	for i := 0; i < 3; i++ {
		value, err := strconv.ParseUint(parts[i+1], 10, 64)
		if err != nil {
			return version, false
		}
		version[i] = value
	}
	return version, true
}

func newer(a, b [3]uint64) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// Check caches all outcomes, including network failures. Refreshing pages cannot
// fan out unlimited requests or bypass the backend's version policy.
func (s *Service) Check(ctx context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if now.Before(s.expires) {
		return s.cached
	}
	result := Status{Current: s.current, State: "check_failed", CheckedAt: now,
		RepositoryURL: RepositoryURL, ReleaseURL: RepositoryURL + "/releases",
		Message: "暂时无法连接 GitHub，版本尚未核实；请稍后重试或前往仓库查看。"}
	s.expires = now.Add(time.Minute)
	current, valid := parseVersion(s.current)
	if !valid {
		result.State = "unpublished"
		result.Message = "当前为开发构建，请使用 GitHub 正式发布版本。"
		s.cached = result
		return result
	}
	entries, err := s.fetch(ctx)
	if err == nil {
		latest := current
		latestTag := ""
		seen := make(map[string]bool)
		for _, entry := range entries {
			version, ok := parseVersion(entry.Tag)
			if !ok || entry.Draft || entry.Prerelease || entry.PublishedAt == nil || seen[entry.Tag] {
				continue
			}
			seen[entry.Tag] = true
			if latestTag == "" || newer(version, latest) {
				latest = version
				latestTag = entry.Tag
			}
			if newer(version, current) {
				result.Behind++
			}
		}
		if latestTag != "" {
			result.Latest = latestTag
			result.ReleaseURL = RepositoryURL + "/releases/tag/" + latestTag
			result.State = "up_to_date"
			result.Message = "当前版本已与 GitHub 正式发布同步。"
			if newer(current, latest) {
				result.State = "unpublished"
				result.Message = "当前版本高于 GitHub 正式发布，尚未验证为正式版本。"
			} else if result.Behind > 0 {
				result.State = "update_available"
				result.CanDefer = result.Behind <= 2
				result.Attention = result.Behind >= 3
				result.BehindAtLeast = len(entries) == 100
				if result.Attention {
					result.State = "attention_required"
				}
				result.Message = "发现新的正式版本，请查看发布说明后保留数据更新。"
				result.UpdateCommand = "curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/update.sh | bash -s -- --version " + latestTag
			}
			s.expires = now.Add(15 * time.Minute)
		}
	}
	s.cached = result
	return result
}

func (s *Service) fetch(ctx context.Context) ([]release, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "HL-panel-version-check")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release service HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil || len(body) > 2*1024*1024 {
		return nil, fmt.Errorf("invalid release response size")
	}
	var entries []release
	if err = json.NewDecoder(strings.NewReader(string(body))).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}
