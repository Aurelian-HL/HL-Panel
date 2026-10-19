package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/faults"
	"io"
	"net/http"
	"regexp"
	"strings"
)

func IsNewer(a, b string) bool {
	av, ok := parseVersion(a)
	bv, bok := parseVersion(b)
	return ok && bok && newer(av, bv)
}

var archiveDigest = regexp.MustCompile(`^sha256:([a-f0-9]{64})$`)

func ValidVersion(tag string) bool { _, ok := parseVersion(tag); return ok }

// ResolveNodeArchive binds a task to an official, compatible immutable archive.
// Compare with the node, permitting a node to catch up to this panel's version.
func (s *Service) ResolveNodeArchive(ctx context.Context, tag, nodeVersion string) (string, error) {
	if _, ok := parseVersion(tag); !ok {
		return "", faults.ErrValidation
	}
	if !ValidVersion(nodeVersion) || (tag != nodeVersion && !IsNewer(tag, nodeVersion)) || IsNewer(tag, s.current) {
		return "", fmt.Errorf("%w: 请选择不低于节点且不高于面板的正式版本", faults.ErrValidation)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/Aurelian-HL/HL-Panel/releases/tags/"+tag, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "HL-panel-node-update")
	response, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: 无法核实正式发布包", faults.ErrConflict)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 || response.StatusCode != 200 {
		return "", faults.ErrConflict
	}
	var item struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Published  string `json:"published_at"`
		Assets     []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
			URL    string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if json.Unmarshal(raw, &item) != nil || item.Tag != tag || item.Draft || item.Prerelease || item.Published == "" {
		return "", faults.ErrValidation
	}
	for _, asset := range item.Assets {
		if asset.Name == "hl-panel-linux-amd64.tar.gz" && asset.URL == RepositoryURL+"/releases/download/"+tag+"/"+asset.Name && archiveDigest.MatchString(asset.Digest) {
			return strings.TrimPrefix(asset.Digest, "sha256:"), nil
		}
	}
	return "", fmt.Errorf("%w: 正式发布包缺少可核实的 SHA256 摘要", faults.ErrValidation)
}
