// Package migrationbackup owns encrypted, portable panel business backups.
package migrationbackup

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/panelruntime"
	"golang.org/x/crypto/scrypt"
)

const MaxArchiveBytes = 64 << 20
const MaxExpandedBytes = 128 << 20
const magic = "HL-PANEL-BACKUP\x01"

// Tables are compiled application schema identifiers, never archive-controlled SQL.
func Tables() []string {
	return []string{"usage_ingest_cursors", "usage_events", "usage_customer_totals", "usage_enforcement_decisions", "usage_enforcement_results"}
}

type RuntimeSecrets struct {
	PasswordFingerprintKey []byte                  `json:"password_fingerprint_key"`
	GatewayPoolTokens      map[string]string       `json:"gateway_pool_tokens"`
	Logs                   []panelruntime.LogEntry `json:"logs"`
}
type State struct {
	Snapshot json.RawMessage            `json:"snapshot"`
	Usage    map[string]json.RawMessage `json:"usage"`
}
type Manifest struct {
	Format    int               `json:"format"`
	Version   string            `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	SourceURL string            `json:"source_url"`
	SHA256    map[string]string `json:"sha256"`
}
type Bundle struct {
	Manifest Manifest
	State    State
	Runtime  RuntimeSecrets
}
type Preview struct {
	Manifest Manifest       `json:"manifest"`
	Digest   string         `json:"digest"`
	Counts   map[string]int `json:"counts"`
}

func invalid(message string) error { return fmt.Errorf("%w: %s", faults.ErrValidation, message) }
func Digest(raw []byte) string     { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

func Seal(bundle Bundle, password string) ([]byte, error) {
	if len(password) < 10 || len(password) > 256 {
		return nil, invalid("备份密码需为 10 至 256 个字符")
	}
	if err := Validate(bundle, bundle.Manifest.Version); err != nil {
		return nil, err
	}
	parts := map[string][]byte{"control.json": bundle.State.Snapshot}
	usage, err := json.Marshal(bundle.State.Usage)
	if err != nil {
		return nil, fmt.Errorf("encode usage backup: %w", err)
	}
	runtimeSecrets, err := json.Marshal(bundle.Runtime)
	if err != nil {
		return nil, fmt.Errorf("encode runtime backup: %w", err)
	}
	parts["usage.json"] = usage
	parts["runtime.json"] = runtimeSecrets
	bundle.Manifest.SHA256 = map[string]string{}
	for name, raw := range parts {
		bundle.Manifest.SHA256[name] = Digest(raw)
	}
	manifest, err := json.Marshal(bundle.Manifest)
	if err != nil {
		return nil, fmt.Errorf("encode backup manifest: %w", err)
	}
	parts["manifest.json"] = manifest
	total := 0
	limits := partLimits()
	for name, raw := range parts {
		total += len(raw)
		if int64(len(raw)) > limits[name] || total > MaxExpandedBytes {
			return nil, invalid("备份内容超过容量限制，请整理历史数据后重试")
		}
	}
	var compressed bytes.Buffer
	z := zip.NewWriter(&compressed)
	for _, name := range []string{"manifest.json", "control.json", "usage.json", "runtime.json"} {
		f, err := z.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err = f.Write(parts[name]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() > MaxArchiveBytes-128 {
		return nil, invalid("备份超过 64 MB，请先联系管理员整理历史数据")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	header := append(append([]byte(magic), salt...), nonce...)
	return gcm.Seal(header, nonce, compressed.Bytes(), header), nil
}
func Open(raw []byte, password, targetVersion string) (Bundle, error) {
	var b Bundle
	headerSize := len(magic) + 16 + 12
	if len(raw) > MaxArchiveBytes || len(raw) < headerSize+16 || !bytes.HasPrefix(raw, []byte(magic)) {
		return b, invalid("不是有效的 HL-Panel 迁移备份包，或文件超过 64 MB")
	}
	if len(password) < 10 || len(password) > 256 {
		return b, invalid("请填写正确的备份密码")
	}
	key, err := scrypt.Key([]byte(password), raw[len(magic):len(magic)+16], 32768, 8, 1, 32)
	if err != nil {
		return b, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return b, invalid("备份加密参数无效")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return b, invalid("备份加密参数无效")
	}
	plain, err := gcm.Open(nil, raw[len(magic)+16:headerSize], raw[headerSize:], raw[:headerSize])
	if err != nil {
		return b, invalid("备份密码错误或文件已损坏")
	}
	z, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil || len(z.File) != 4 {
		return b, invalid("备份压缩包结构无效")
	}
	parts := map[string][]byte{}
	total := int64(0)
	allowed := partLimits()
	for _, f := range z.File {
		limit, ok := allowed[f.Name]
		if !ok || parts[f.Name] != nil || f.Mode()&fs.ModeType != 0 {
			return b, invalid("备份包含无效或重复文件")
		}
		if f.UncompressedSize64 > uint64(limit) {
			return b, invalid("备份解压大小超出限制")
		}
		reader, e := f.Open()
		if e != nil {
			return b, invalid("备份文件无法读取")
		}
		data, e := io.ReadAll(io.LimitReader(reader, limit+1))
		reader.Close()
		total += int64(len(data))
		if e != nil || int64(len(data)) > limit || total > MaxExpandedBytes {
			return b, invalid("备份解压失败或超出限制")
		}
		parts[f.Name] = data
	}
	if err = json.Unmarshal(parts["manifest.json"], &b.Manifest); err != nil {
		return b, invalid("备份清单无效")
	}
	for _, name := range []string{"control.json", "usage.json", "runtime.json"} {
		if b.Manifest.SHA256[name] != Digest(parts[name]) {
			return b, invalid("备份内容校验失败")
		}
	}
	b.State.Snapshot = parts["control.json"]
	if json.Unmarshal(parts["usage.json"], &b.State.Usage) != nil || json.Unmarshal(parts["runtime.json"], &b.Runtime) != nil {
		return b, invalid("备份数据格式无效")
	}
	return b, Validate(b, targetVersion)
}
func Validate(b Bundle, targetVersion string) error {
	if b.Manifest.Format != 1 || b.Manifest.CreatedAt.IsZero() {
		return invalid("不支持的备份格式")
	}
	source, ok := version(b.Manifest.Version)
	target, valid := version(targetVersion)
	if !ok || !valid {
		return invalid("迁移要求使用正式发布版本")
	}
	for i := range source {
		if source[i] > target[i] {
			return invalid("新面板版本低于备份版本，请先更新新面板")
		}
		if source[i] < target[i] {
			break
		}
	}
	u, err := url.Parse(b.Manifest.SourceURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return invalid("原面板地址格式无效")
	}
	if _, err = memoryrepo.DecodeSnapshot(b.State.Snapshot); err != nil {
		return invalid("面板数据损坏或版本不兼容")
	}
	if len(b.Runtime.PasswordFingerprintKey) < 32 || len(b.Runtime.PasswordFingerprintKey) > 1024 || len(b.Runtime.GatewayPoolTokens) > 1024 || len(b.Runtime.Logs) > 2000 {
		return invalid("备份运行密钥或日志无效")
	}
	for pool, token := range b.Runtime.GatewayPoolTokens {
		if pool == "" || len(pool) > 128 || len(token) < 32 || len(token) > 256 || strings.ContainsAny(token, " \r\n\t") {
			return invalid("备份网关密钥无效")
		}
	}
	if len(b.State.Usage) != len(Tables()) {
		return invalid("备份缺少完整流量账本")
	}
	for _, table := range Tables() {
		raw, ok := b.State.Usage[table]
		var rows []map[string]json.RawMessage
		if !ok || len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &rows) != nil {
			return invalid("备份流量账本格式无效")
		}
		for _, row := range rows {
			if len(row) != len(ledgerColumns[table]) {
				return invalid("流量账本字段与当前版本不兼容")
			}
			for _, column := range ledgerColumns[table] {
				if _, ok := row[column]; !ok {
					return invalid("流量账本缺少字段")
				}
			}
		}
	}
	return nil
}
func Describe(b Bundle, digest string) Preview {
	var data map[string]json.RawMessage
	_ = json.Unmarshal(b.State.Snapshot, &data)
	counts := map[string]int{}
	for label, key := range map[string]string{"administrators": "Administrators", "customers": "Customers", "device_groups": "DeviceGroups", "nodes": "Nodes", "rules": "ForwardRules", "subscriptions": "Subscriptions", "rule_groups": "RuleGroups", "user_groups": "UserGroups"} {
		var entries map[string]json.RawMessage
		_ = json.Unmarshal(data[key], &entries)
		counts[label] = len(entries)
	}
	for _, table := range Tables() {
		var rows []json.RawMessage
		_ = json.Unmarshal(b.State.Usage[table], &rows)
		counts[table] = len(rows)
	}
	return Preview{Manifest: b.Manifest, Digest: digest, Counts: counts}
}
func partLimits() map[string]int64 {
	return map[string]int64{"manifest.json": 8192, "control.json": memoryrepo.MaxSnapshotBytes, "usage.json": MaxExpandedBytes - memoryrepo.MaxSnapshotBytes - (4 << 20) - 8192, "runtime.json": 4 << 20}
}

var ledgerColumns = map[string][]string{
	"usage_ingest_cursors":        strings.Fields("node_id boot_id last_sequence updated_at"),
	"usage_events":                strings.Fields("node_id boot_id sequence payload_sha256 customer_id rule_id entry_group_id exit_group_id protocol occurred_at period_started_at period_ended_at rule_actual_bytes customer_actual_bytes charged_bytes entry_multiplier_micros exit_multiplier_micros received_at"),
	"usage_customer_totals":       strings.Fields("customer_id actual_bytes charged_bytes last_usage_occurred_at updated_at"),
	"usage_enforcement_decisions": strings.Fields("id customer_id rule_id protocol reason action status trigger_node_id trigger_boot_id trigger_sequence customer_charged_bytes traffic_limit_bytes created_at updated_at revision last_error revoke_administrator_id revoke_idempotency_key revoke_request_sha256 revoke_requested_at"),
	"usage_enforcement_results":   strings.Fields("decision_id command_revision node_id action result_status payload_sha256 sanitized_message created_at"),
}

func version(s string) ([3]int, bool) {
	var n [3]int
	p := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(p) != 3 {
		return n, false
	}
	for i, v := range p {
		x, e := strconv.Atoi(v)
		if e != nil || x < 0 {
			return n, false
		}
		n[i] = x
	}
	return n, true
}
