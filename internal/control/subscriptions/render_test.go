package subscriptions

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"github.com/hongle/hl-panel/internal/control/faults"
	"gopkg.in/yaml.v3"
	"image/png"
	"io"
	"strings"
	"testing"
)

const testURI = "vless://a612b608-291f-4add-ae78-ece0c450691a@edge.example.test:443?security=tls&type=tcp&sni=edge.example.test"

func TestRenderRoundTripAndPackage(t *testing.T) {
	lines := []Resolved{{Name: "随遇而安", URI: testURI}, {Name: "随遇而安-选择", URI: "socks5://user:p%40ss@[::1]:1080"}, {Name: "offline", Error: "unavailable"}}
	txt, err := base64.StdEncoding.DecodeString(TXT(lines))
	if err != nil || len(strings.Split(string(txt), "\n")) != 2 {
		t.Fatal("TXT did not preserve two available lines")
	}
	body, err := YAML(lines)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []map[string]any `yaml:"proxy-groups"`
	}
	if err = yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Proxies) != 2 || config.Proxies[0]["name"] != "随遇而安" || config.Proxies[1]["password"] != "p@ss" || config.Groups[0]["name"] != "随遇而安-选择 2" || bytes.Contains(body, []byte("offline")) {
		t.Fatal("unsafe group name or malformed YAML")
	}
	r := Record{Item: Item{Name: "随遇而安"}, Token: "sub_" + strings.Repeat("A", 43)}
	pack, err := Package(r, lines, "https://panel.example.test")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(pack), int64(len(pack)))
	if err != nil || len(z.File) != 6 {
		t.Fatal("invalid import ZIP")
	}
	for _, f := range z.File {
		if !strings.HasPrefix(f.Name, "随遇而安-") || f.Flags&0x800 == 0 {
			t.Fatal("rule name or UTF-8 ZIP flag missing", f.Name)
		}
		reader, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(reader)
		reader.Close()
		if e != nil {
			t.Fatal(e)
		}
		switch f.Name {
		case "随遇而安-苹果小火箭订阅二维码.png":
			if _, e = png.Decode(bytes.NewReader(raw)); e != nil {
				t.Fatal(e)
			}
		case "随遇而安-电脑手机通用v2rayN订阅链接.txt", "随遇而安-小火箭订阅链接.txt":
			if string(raw) != "https://panel.example.test"+Paths(r.Token).TXT+"\r\n" || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
				t.Fatal("wrong feed address")
			}
		case "随遇而安-Clash订阅链接.txt":
			if string(raw) != "https://panel.example.test"+Paths(r.Token).YAML+"\r\n" || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
				t.Fatal("wrong Clash feed address")
			}
		case "随遇而安-电脑Clash直接拖入使用.yaml":
			var parsed any
			if err := yaml.Unmarshal(raw, &parsed); err != nil {
				t.Fatal("BOM YAML unreadable", err)
			}
		}
	}
	for _, origin := range []string{"https://user:pass@example.test", "https://example.test/path", "https://example.test?x=1", "file:///tmp/x"} {
		if _, err = Package(r, lines, origin); err == nil {
			t.Fatal("invalid origin accepted")
		}
	}
	empty, _ := YAML([]Resolved{{Name: "offline", Error: "unavailable"}})
	if !bytes.Contains(empty, []byte("MATCH,REJECT")) || bytes.Contains(empty, []byte("DIRECT")) {
		t.Fatal("empty subscription must fail closed")
	}
	if _, err := Package(r, []Resolved{{Error: "规则已暂停"}}, "https://panel.example.test"); !errors.Is(err, faults.ErrConflict) || !strings.Contains(err.Error(), "规则已暂停") {
		t.Fatal("empty ZIP not rejected with reason", err)
	}
}

func TestPackageFilenameAndYAMLInjection(t *testing.T) {
	for raw, want := range map[string]string{"../../a:b": "_.._a_b", "CON": "_CON", "NUL.txt": "_NUL.txt", " . ": "HL-panel", "随遇而安": "随遇而安"} {
		if got := PackageBaseName(raw); got != want {
			t.Errorf("name %q got %q want %q", raw, got, want)
		}
	}
	if len(PackageBaseName(strings.Repeat("中", 128))) > 180 {
		t.Fatal("filename exceeds filesystem byte limit")
	}
	name := "线路: # [\"quoted\"]\nproxies: []"
	body, err := YAML([]Resolved{{Name: name, URI: testURI}})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = yaml.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if len(value["proxies"].([]any)) != 1 || value["proxies"].([]any)[0].(map[string]any)["name"] != name {
		t.Fatal("YAML name injection")
	}
}

func TestProxyValidationAndSOCKSReparse(t *testing.T) {
	for _, uri := range []string{testURI, "socks5://user:p%3Aa%40ss@[2001:db8::1]:1080", "socks5://host.example.test:1080"} {
		p, err := ParseProxy(uri, "线路")
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseProxy(p.URI, "线路")
		if err != nil || again.Config["type"] != p.Config["type"] {
			t.Fatal("URI not roundtrippable")
		}
	}
	for _, uri := range []string{testURI + "&type=ws", testURI + "&foo=bar", testURI + "&sni=%ZZ", testURI + "&encryption=aes", strings.Replace(testURI, "security=tls", "security=reality", 1), "socks5://user:%0A@host.test:1080", "http://example.test:80", "socks5://user:pass@host.test:0"} {
		if _, err := ParseProxy(uri, "line"); err == nil {
			t.Fatal("invalid proxy accepted")
		}
	}
}
