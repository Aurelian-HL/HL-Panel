package subscriptions

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/faults"
	qrcode "github.com/skip2/go-qrcode"
	"gopkg.in/yaml.v3"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type Proxy struct {
	URI    string
	Config map[string]any
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var shortIDPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{2}){1,8}$`)

func invalidLine() error {
	return fmt.Errorf("%w: 仅支持完整的 VLESS TCP（Reality/TLS/无 TLS）或 SOCKS5 链接", faults.ErrValidation)
}

// ParseProxy never fetches a URL. Unsupported transports/options are rejected
// rather than silently producing a different client configuration.
func ParseProxy(raw, name string) (Proxy, error) {
	if len(raw) > 8192 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return Proxy{}, invalidLine()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.Path != "" || strings.ContainsAny(u.Hostname(), " /\\\t\n\r") {
		return Proxy{}, invalidLine()
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return Proxy{}, invalidLine()
	}
	p := map[string]any{"name": name, "server": u.Hostname(), "port": port}
	switch strings.ToLower(u.Scheme) {
	case "vless":
		if u.User == nil || !uuidPattern.MatchString(u.User.Username()) {
			return Proxy{}, invalidLine()
		}
		if _, ok := u.User.Password(); ok {
			return Proxy{}, invalidLine()
		}
		q, queryErr := url.ParseQuery(u.RawQuery)
		if queryErr != nil {
			return Proxy{}, invalidLine()
		}
		allowed := map[string]bool{"type": true, "security": true, "encryption": true, "flow": true, "sni": true, "fp": true, "pbk": true, "sid": true, "headerType": true, "alpn": true, "spx": true}
		for k, v := range q {
			if !allowed[k] || len(v) != 1 {
				return Proxy{}, invalidLine()
			}
		}
		if q.Get("type") != "" && q.Get("type") != "tcp" || q.Get("headerType") != "" && q.Get("headerType") != "none" || q.Get("encryption") != "" && q.Get("encryption") != "none" {
			return Proxy{}, invalidLine()
		}
		p["type"] = "vless"
		p["uuid"] = u.User.Username()
		p["network"] = "tcp"
		p["udp"] = true
		security := q.Get("security")
		if security == "" {
			security = "none"
		}
		if security != "none" && security != "tls" && security != "reality" {
			return Proxy{}, invalidLine()
		}
		p["tls"] = security != "none"
		if alpn := q.Get("alpn"); alpn != "" {
			if security != "tls" {
				return Proxy{}, invalidLine()
			}
			values := strings.Split(alpn, ",")
			for _, value := range values {
				if value != "h2" && value != "http/1.1" {
					return Proxy{}, invalidLine()
				}
			}
			p["alpn"] = values
		}
		if q.Get("spx") != "" && q.Get("spx") != "/" {
			// Mihomo cannot express Xray's custom Reality spider path.
			return Proxy{}, invalidLine()
		}
		if security != "reality" && (q.Get("pbk") != "" || q.Get("sid") != "" || q.Get("spx") != "") {
			return Proxy{}, invalidLine()
		}
		if flow := q.Get("flow"); flow != "" {
			if flow != "xtls-rprx-vision" || security == "none" {
				return Proxy{}, invalidLine()
			}
			p["flow"] = flow
		}
		if sni := q.Get("sni"); sni != "" {
			p["servername"] = sni
		}
		if fp := q.Get("fp"); fp != "" {
			switch fp {
			case "chrome", "firefox", "safari", "ios", "android", "edge", "random", "randomized":
				p["client-fingerprint"] = fp
			default:
				return Proxy{}, invalidLine()
			}
		}
		if security == "reality" {
			pbk, e := base64.RawURLEncoding.DecodeString(q.Get("pbk"))
			if e != nil || len(pbk) != 32 || !shortIDPattern.MatchString(q.Get("sid")) || q.Get("sni") == "" {
				return Proxy{}, invalidLine()
			}
			p["reality-opts"] = map[string]string{"public-key": q.Get("pbk"), "short-id": q.Get("sid")}
			if q.Get("fp") == "" {
				p["client-fingerprint"] = "chrome"
			}
		}
	case "socks", "socks5":
		if u.RawQuery != "" || u.ForceQuery {
			return Proxy{}, invalidLine()
		}
		p["type"] = "socks5"
		p["udp"] = true
		if u.User != nil {
			username := u.User.Username()
			password, ok := u.User.Password()
			if !ok && strings.EqualFold(u.Scheme, "socks") {
				decoded, e := base64.RawURLEncoding.DecodeString(username)
				if e != nil {
					decoded, e = base64.StdEncoding.DecodeString(username)
				}
				if e != nil {
					return Proxy{}, invalidLine()
				}
				username, password, ok = strings.Cut(string(decoded), ":")
			}
			if !ok || username == "" || strings.IndexFunc(username+password, unicode.IsControl) >= 0 {
				return Proxy{}, invalidLine()
			}
			p["username"] = username
			p["password"] = password
			u.Scheme = "socks"
			u.User = url.User(base64.RawURLEncoding.EncodeToString([]byte(username + ":" + password)))
		}
	default:
		return Proxy{}, invalidLine()
	}
	u.Fragment = name
	return Proxy{URI: u.String(), Config: p}, nil
}
func TXT(lines []Resolved) string {
	uris := []string{}
	for _, line := range lines {
		if line.URI != "" {
			uris = append(uris, line.URI)
		}
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(uris, "\n")))
}
func YAML(lines []Resolved) ([]byte, error) {
	proxies := []map[string]any{}
	names := []string{}
	for _, line := range lines {
		if line.URI == "" {
			continue
		}
		p, err := ParseProxy(line.URI, line.Name)
		if err != nil {
			return nil, err
		}
		proxies = append(proxies, p.Config)
		names = append(names, line.Name)
	}
	document := struct {
		MixedPort int              `yaml:"mixed-port"`
		AllowLAN  bool             `yaml:"allow-lan"`
		Mode      string           `yaml:"mode"`
		LogLevel  string           `yaml:"log-level"`
		Proxies   []map[string]any `yaml:"proxies"`
		Groups    []map[string]any `yaml:"proxy-groups"`
		Rules     []string         `yaml:"rules"`
	}{7890, false, "rule", "info", proxies, []map[string]any{}, []string{"MATCH,REJECT"}}
	if len(proxies) > 0 {
		base := names[0] + "-选择"
		groupName := base
		// Names containing commas cannot be used as a Clash routing target.
		if strings.ContainsAny(base, ",\r\n") {
			groupName = "HL-panel-选择"
			base = groupName
		}
		for suffix := 2; containsName(names, groupName); suffix++ {
			groupName = fmt.Sprintf("%s %d", base, suffix)
		}
		document.Groups = []map[string]any{{"name": groupName, "type": "select", "proxies": names}}
		document.Rules = []string{"MATCH," + groupName}
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

type Links struct {
	TXT  string `json:"txt"`
	YAML string `json:"yaml"`
}

func Paths(token string) Links {
	base := "/api/v1/public/subscriptions/" + token
	return Links{TXT: base + ".txt", YAML: base + ".yaml"}
}
func FeedURL(token, baseURL, format string) (string, error) {
	u, err := url.Parse(baseURL)
	if !tokenPattern.MatchString(token) || (format != "txt" && format != "yaml") || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || net.ParseIP(u.Hostname()) == nil && strings.ContainsAny(u.Hostname(), " /\\") {
		return "", faults.ErrValidation
	}
	origin := u.Scheme + "://" + u.Host
	return origin + "/api/v1/public/subscriptions/" + token + "." + format, nil
}
func QRCode(token, baseURL, format string) ([]byte, error) {
	address, err := FeedURL(token, baseURL, format)
	if err != nil {
		return nil, err
	}
	return qrcode.Encode(address, qrcode.Medium, 384)
}
func Package(r Record, lines []Resolved, baseURL string) ([]byte, error) {
	available := false
	for _, line := range lines {
		available = available || line.URI != ""
	}
	if !available {
		message := "没有可用线路，请检查规则、客户和节点状态"
		if len(lines) == 1 && lines[0].Error != "" {
			message = lines[0].Error
		}
		return nil, fmt.Errorf("%w: 无法下载订阅包：%s", faults.ErrConflict, message)
	}
	txtURL, err := FeedURL(r.Token, baseURL, "txt")
	if err != nil {
		return nil, err
	}
	yamlURL, err := FeedURL(r.Token, baseURL, "yaml")
	if err != nil {
		return nil, err
	}
	yaml, err := YAML(lines)
	if err != nil {
		return nil, err
	}
	qr, err := QRCode(r.Token, baseURL, "txt")
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	name := PackageBaseName(r.Item.Name)
	// UTF-8 BOM lets Windows text editors detect Chinese reliably. The public
	// feed remains UTF-8 without BOM for subscription client compatibility.
	textFile := func(value string) []byte { return []byte("\xef\xbb\xbf" + strings.ReplaceAll(value, "\n", "\r\n")) }
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{name + "-电脑Clash直接拖入使用.yaml", append([]byte{0xef, 0xbb, 0xbf}, yaml...)},
		{name + "-Clash订阅链接.txt", textFile(yamlURL + "\n")},
		{name + "-电脑手机通用v2rayN订阅链接.txt", textFile(txtURL + "\n")},
		{name + "-小火箭订阅链接.txt", textFile(txtURL + "\n")},
		{name + "-苹果小火箭订阅二维码.png", qr},
		{name + "-导入使用说明.txt", textFile("HL-panel 订阅导入包\n规则 / 订阅：" + r.Item.Name + "\n请先解压 ZIP。\nClash/Mihomo：导入同名 YAML 文件，或从 Clash订阅链接.txt 中复制地址并从 URL 导入。\nv2rayN：使用电脑手机通用v2rayN订阅链接.txt 中的地址。\nShadowrocket（小火箭）：使用小火箭订阅链接.txt 中的地址，或扫描苹果小火箭订阅二维码.png。\n二维码内容为 TXT 订阅地址。静态 YAML 为下载时的快照；需要自动更新请使用订阅地址。\n设备组入口域名修改后，原生线路在客户端刷新订阅时更新，订阅地址不变。手工链接需自行修改并发布；已导入的静态 YAML 或单条节点链接需重新导入。\n面板域名更换且旧地址无法访问时，需重新导入新面板的订阅地址。\n规则暂停、额度用完或尚未就绪时暂不下发该线路；恢复后请更新客户端订阅。\n订阅地址含访问凭据，请勿公开。\n")},
	} {
		f, e := writer.Create(entry.name)
		if e != nil {
			return nil, e
		}
		if _, e = f.Write(entry.data); e != nil {
			return nil, e
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Strip Windows-invalid characters, paths and device names. Truncation uses
// UTF-16 code units, keeping all six filenames usable in Windows Explorer.
func PackageBaseName(value string) string {
	var out strings.Builder
	units := 0
	for _, r := range value {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > 80 {
			break
		}
		if out.Len()+len(string(r)) > 180 {
			break
		}
		out.WriteRune(r)
		units += size
	}
	name := strings.Trim(out.String(), " .")
	if name == "" {
		name = "HL-panel"
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || regexp.MustCompile(`^(COM|LPT)[1-9¹²³]$`).MatchString(stem) {
		name = "_" + name
	}
	return name
}
