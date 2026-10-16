// Package importedconfig validates and compiles confidential imported engine
// material without accessing external services or creating control-plane state.
package importedconfig

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hongle/hl-panel/internal/control/faults"
)

const MaxArchiveBytes = 8 << 20

// Archive is confidential. Only Plan may be returned by HTTP or logging code.
// Keeping the full native configuration avoids silently dropping engine options.
type Archive struct {
	Schema        string          `json:"schema"`
	SourceID      string          `json:"source_id"`
	Host          string          `json:"host"`
	ObservedAt    time.Time       `json:"observed_at"`
	Inbounds      []SourceInbound `json:"inbounds"`
	Config        json.RawMessage `json:"config"`
	Subscriptions json.RawMessage `json:"subscriptions"`
}

type SourceInbound struct {
	ID      int64           `json:"id"`
	Port    int             `json:"port"`
	Name    string          `json:"name"`
	OwnerID string          `json:"owner_id"`
	Raw     json.RawMessage `json:"raw"`
}

type Line struct {
	SourceID        int64  `json:"source_id"`
	Name            string `json:"name"`
	Port            int    `json:"port"`
	OwnerID         string `json:"owner_id,omitempty"`
	Outbound        string `json:"outbound"`
	ShortIDCount    int    `json:"short_id_count"`
	ServerNameCount int    `json:"server_name_count"`
	EmptyFlow       bool   `json:"empty_flow"`
	MLDSA           bool   `json:"mldsa"`
}

type Plan struct {
	SourceID          string    `json:"source_id"`
	Host              string    `json:"host"`
	ObservedAt        time.Time `json:"observed_at"`
	Lines             []Line    `json:"lines"`
	Unassigned        int       `json:"unassigned"`
	State             string    `json:"state"`
	SubscriptionCount int       `json:"subscription_count"`
}

func invalid(reason string) error {
	// Reasons are fixed strings: no source values, JSON decoder excerpts or keys.
	return fmt.Errorf("%w: 迁移文件%s", faults.ErrValidation, reason)
}

func decode(raw []byte, target any) error {
	if !uniqueJSON(raw) {
		return invalid("格式无效或包含重复字段")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(target) != nil {
		return invalid("格式无效")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return invalid("包含多余内容")
	}
	return nil
}

func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	var value map[string]json.RawMessage
	// The KL API encodes nested settings as JSON strings.
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, invalid("配置无效")
		}
		raw = []byte(text)
	}
	if decode(raw, &value) != nil || value == nil {
		return nil, invalid("配置无效")
	}
	return value, nil
}

func text(raw json.RawMessage) string  { var v string; _ = json.Unmarshal(raw, &v); return v }
func number(raw json.RawMessage) int64 { var v int64; _ = json.Unmarshal(raw, &v); return v }
func list[T any](raw json.RawMessage) ([]T, error) {
	var v []T
	err := decode(raw, &v)
	if v == nil && err == nil {
		err = invalid("列表无效")
	}
	return v, err
}
func identifier(v string) bool {
	return len(v) > 0 && len(v) <= 128 && strings.IndexFunc(v, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.')
	}) < 0
}
func printable(v string, max int) bool {
	return len(v) <= max && strings.IndexFunc(v, unicode.IsControl) < 0
}
func equalJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var x, y any
	if decode(a, &x) != nil || decode(b, &y) != nil {
		return false
	}
	xx, _ := json.Marshal(x)
	yy, _ := json.Marshal(y)
	return bytes.Equal(xx, yy)
}

// Validate compares API credentials with the effective native engine config.
// No default UUID, flow, routing, SNI, shortID or traffic value is generated.
func Validate(raw []byte) (Plan, error) {
	var a Archive
	if len(raw) == 0 || len(raw) > MaxArchiveBytes {
		return Plan{}, invalid("大小超限")
	}
	if err := decode(raw, &a); err != nil {
		return Plan{}, err
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if strict.Decode(&a) != nil {
		return Plan{}, invalid("结构无效")
	}
	if a.Schema != "hl-panel.imported-config/v1" || !identifier(a.SourceID) || a.Host != "source.example.invalid" || a.ObservedAt.IsZero() || len(a.Inbounds) == 0 || len(a.Inbounds) > 500 {
		return Plan{}, invalid("来源或版本无效")
	}
	c, err := object(a.Config)
	if err != nil {
		return Plan{}, err
	}
	ins, err := list[map[string]json.RawMessage](c["inbounds"])
	if err != nil {
		return Plan{}, err
	}
	outs, err := list[map[string]json.RawMessage](c["outbounds"])
	if err != nil || len(outs) == 0 {
		return Plan{}, invalid("缺少出口")
	}
	routing, err := object(c["routing"])
	if err != nil {
		return Plan{}, err
	}
	routes, err := list[map[string]json.RawMessage](routing["rules"])
	if err != nil {
		return Plan{}, err
	}
	if b := routing["balancers"]; len(b) > 0 && string(b) != "[]" {
		return Plan{}, invalid("包含未支持的均衡策略")
	}
	byPort := map[int]map[string]json.RawMessage{}
	byTag := map[string]string{}
	outObjects := map[string]map[string]json.RawMessage{}
	inTags := map[string]bool{}
	seenUUID := map[string]bool{}
	seenID := map[int64]bool{}
	for _, o := range outs {
		tag := text(o["tag"])
		if tag == "" || byTag[tag] != "" {
			return Plan{}, invalid("出口标记冲突")
		}
		byTag[tag] = text(o["protocol"])
		outObjects[tag] = o
	}
	for _, i := range ins {
		if text(i["protocol"]) != "vless" {
			continue
		}
		tag := text(i["tag"])
		if tag == "" || inTags[tag] {
			return Plan{}, invalid("入站标记冲突")
		}
		inTags[tag] = true
		p := int(number(i["port"]))
		if p < 1 || p > 65535 || byPort[p] != nil {
			return Plan{}, invalid("监听端口冲突")
		}
		byPort[p] = i
	}
	if len(byPort) != len(a.Inbounds) {
		return Plan{}, invalid("入站数量不一致")
	}
	p := Plan{SourceID: a.SourceID, Host: a.Host, ObservedAt: a.ObservedAt, State: "pending_configuration", Lines: []Line{}}
	for _, row := range a.Inbounds {
		i := byPort[row.Port]
		if row.ID <= 0 || seenID[row.ID] || i == nil || !printable(row.Name, 512) || (row.OwnerID != "" && !identifier(row.OwnerID)) {
			return Plan{}, invalid("线路标识无效或重复")
		}
		seenID[row.ID] = true
		delete(byPort, row.Port)
		source, err := object(row.Raw)
		if err != nil {
			return Plan{}, err
		}
		if number(source["id"]) != row.ID || int(number(source["port"])) != row.Port || text(source["protocol"]) != "vless" {
			return Plan{}, invalid("线路来源不一致")
		}
		settings, err := object(i["settings"])
		if err != nil {
			return Plan{}, err
		}
		clients, err := list[map[string]json.RawMessage](settings["clients"])
		if err != nil || len(clients) != 1 || text(settings["decryption"]) != "none" {
			return Plan{}, invalid("客户凭据结构暂不支持")
		}
		uuid := text(clients[0]["id"])
		if !validUUID(uuid) || seenUUID[strings.ToLower(uuid)] {
			return Plan{}, invalid("客户凭据无效或重复")
		}
		seenUUID[strings.ToLower(uuid)] = true
		ss, err := object(source["settings"])
		if err != nil {
			return Plan{}, err
		}
		sc, err := list[map[string]json.RawMessage](ss["clients"])
		if err != nil || len(sc) != 1 || !equalJSON(sc[0]["id"], clients[0]["id"]) || text(sc[0]["flow"]) != text(clients[0]["flow"]) {
			return Plan{}, invalid("客户凭据与引擎不一致")
		}
		if flow := text(clients[0]["flow"]); flow != "" && flow != "xtls-rprx-vision" {
			return Plan{}, invalid("流控参数暂不支持")
		}
		stream, err := object(i["streamSettings"])
		if err != nil {
			return Plan{}, err
		}
		if text(stream["network"]) != "tcp" || text(stream["security"]) != "reality" {
			return Plan{}, invalid("传输协议暂不支持")
		}
		reality, err := object(stream["realitySettings"])
		if err != nil {
			return Plan{}, err
		}
		apiStream, err := object(source["streamSettings"])
		if err != nil {
			return Plan{}, err
		}
		apiReality, err := object(apiStream["realitySettings"])
		if err != nil {
			return Plan{}, err
		}
		if text(apiStream["network"]) != text(stream["network"]) || text(apiStream["security"]) != text(stream["security"]) {
			return Plan{}, invalid("传输参数与引擎不一致")
		}
		for _, field := range []string{"sniffing"} {
			if len(source[field]) > 0 {
				left, e := object(source[field])
				if e != nil {
					return Plan{}, e
				}
				right, e := object(i[field])
				if e != nil || !equalMaps(left, right) {
					return Plan{}, invalid("嗅探参数与引擎不一致")
				}
			}
		}
		for _, key := range []string{"privateKey", "shortIds", "serverNames", "mldsa65Seed"} {
			if !equalJSON(reality[key], apiReality[key]) {
				return Plan{}, invalid("握手参数与引擎不一致")
			}
		}
		key, err := base64.RawURLEncoding.DecodeString(text(reality["privateKey"]))
		if err != nil || len(key) != 32 {
			return Plan{}, invalid("握手密钥无效")
		}
		if _, err = ecdh.X25519().NewPrivateKey(key); err != nil {
			return Plan{}, invalid("握手密钥无效")
		}
		names, err := list[string](reality["serverNames"])
		if err != nil || len(names) == 0 || len(names) > 16 {
			return Plan{}, invalid("握手域名无效")
		}
		for _, name := range names {
			if name == "" || !printable(name, 253) || strings.ContainsAny(name, " /\\") {
				return Plan{}, invalid("握手域名无效")
			}
		}
		shorts, err := list[string](reality["shortIds"])
		if err != nil || len(shorts) == 0 || len(shorts) > 16 {
			return Plan{}, invalid("握手标识无效")
		}
		for _, short := range shorts {
			if len(short) > 16 || len(short)%2 != 0 {
				return Plan{}, invalid("握手标识无效")
			}
			if _, err := hex.DecodeString(short); err != nil {
				return Plan{}, invalid("握手标识无效")
			}
		}
		seed := text(reality["mldsa65Seed"])
		if seed != "" {
			decoded, err := base64.RawURLEncoding.DecodeString(seed)
			if err != nil || len(decoded) != 32 {
				return Plan{}, invalid("签名密钥无效")
			}
		}
		dest := text(reality["dest"])
		if dest == "" {
			dest = text(reality["target"])
		}
		host, port, err := net.SplitHostPort(dest)
		n, _ := strconv.Atoi(port)
		if err != nil || host == "" || n < 1 || n > 65535 {
			return Plan{}, invalid("握手目标无效")
		}
		outTag := text(outs[0]["tag"])
		matches := 0
		for _, r := range routes {
			tags, _ := list[string](r["inboundTag"])
			for _, tag := range tags {
				if tag == text(i["tag"]) {
					matches++
					if matches > 1 || len(r) != 3 || text(r["type"]) != "field" {
						return Plan{}, invalid("线路路由不明确或暂不支持")
					}
					outTag = text(r["outboundTag"])
				}
			}
		}
		out := byTag[outTag]
		if err := validateOutbound(outObjects[outTag]); err != nil {
			return Plan{}, err
		}
		if out != "socks" && out != "freedom" {
			return Plan{}, invalid("线路出口暂不支持")
		}
		if row.OwnerID == "" {
			p.Unassigned++
		}
		p.Lines = append(p.Lines, Line{SourceID: row.ID, Name: row.Name, Port: row.Port, OwnerID: row.OwnerID, Outbound: out, ShortIDCount: len(shorts), ServerNameCount: len(names), EmptyFlow: text(clients[0]["flow"]) == "", MLDSA: seed != ""})
	}
	var subs []json.RawMessage
	if len(a.Subscriptions) > 0 {
		if err := decode(a.Subscriptions, &subs); err != nil {
			return Plan{}, err
		}
	}
	p.SubscriptionCount = len(subs)
	return p, nil
}

func validUUID(v string) bool {
	if len(v) != 36 || v[8] != '-' || v[13] != '-' || v[18] != '-' || v[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(v, "-", ""))
	return err == nil
}

func Digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
