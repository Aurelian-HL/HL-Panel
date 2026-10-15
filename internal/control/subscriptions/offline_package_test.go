package subscriptions

import (
	"archive/zip"
	"bytes"
	"image/png"
	"io"
	"net/url"
	"path"
	"strings"
	"testing"
)

func TestOfflinePackagePreservesEligibleNodeConfiguration(t *testing.T) {
	lines := []Resolved{
		{Name: "香港/规则", URI: testURI + "#旧名字"},
		{Name: "香港:规则", URI: "socks5://user:p%40ss@[2001:db8::1]:1080"},
		{Name: "未就绪", Error: "规则已暂停"},
	}
	r := Record{Item: Item{Name: "订阅"}, Token: "sub_" + strings.Repeat("A", 43)}
	pack, err := Package(r, lines, "https://panel.example.test")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(pack), int64(len(pack)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range archive.File {
		if path.Clean(f.Name) != f.Name || strings.Contains(f.Name, "\\") {
			t.Fatal("unsafe ZIP path", f.Name)
		}
		if _, exists := files[f.Name]; exists {
			t.Fatal("duplicate ZIP entry", f.Name)
		}
		reader, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = raw
	}
	for i, base := range []string{"离线二维码/001-香港_规则", "离线二维码/002-香港_规则"} {
		raw := files[base+"-单节点链接.txt"]
		proxy, err := ParseProxy(lines[i].URI, lines[i].Name)
		if err != nil || string(raw) != proxy.URI+"\r\n" || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
			t.Fatal("offline link does not preserve the current node configuration")
		}
		u, err := url.Parse(strings.TrimSpace(string(raw)))
		if err != nil || u.Fragment != lines[i].Name || (u.Scheme != "vless" && u.Scheme != "socks") || strings.Contains(string(raw), r.Token) {
			t.Fatal("offline link must be a native URI with the current rule name")
		}
		img, err := png.Decode(bytes.NewReader(files[base+"-节点二维码.png"]))
		if err != nil || img.Bounds().Dx() < 384 || img.Bounds().Dx() != img.Bounds().Dy() {
			t.Fatal("invalid or undersized offline PNG")
		}
	}
	if len(files) != 11 || bytes.Contains(files["离线二维码/使用说明.txt"], []byte(r.Token)) {
		t.Fatal("offline line eligibility or instructions violated")
	}
}

func TestOfflineQRCodeCapacityDoesNotBlockPackageDownload(t *testing.T) {
	line := Resolved{Name: "超长配置", URI: "socks5://user:" + strings.Repeat("a", 4000) + "@edge.example.test:1080"}
	entries, err := offlinePackageEntries([]Resolved{line})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || !strings.HasSuffix(entries[2].name, "-二维码说明.txt") {
		t.Fatal("QR overflow must provide instructions rather than an empty QR")
	}
	proxy, err := ParseProxy(line.URI, line.Name)
	if err != nil || string(entries[1].data) != proxy.URI+"\r\n" {
		t.Fatal("QR overflow truncated the node URI")
	}
	r := Record{Item: Item{Name: "订阅"}, Token: "sub_" + strings.Repeat("A", 43)}
	if _, err := Package(r, []Resolved{line}, "https://panel.example.test"); err != nil {
		t.Fatal("QR capacity must not break a valid subscription package", err)
	}
	if _, err := offlinePackageEntries([]Resolved{{Name: "错误配置", URI: "https://panel.example.test"}}); err == nil {
		t.Fatal("offline export accepted an unsupported protocol")
	}
}
