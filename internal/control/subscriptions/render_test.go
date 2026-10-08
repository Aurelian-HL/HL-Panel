package subscriptions

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"io"
	"strings"
	"testing"
)

const testURI = "vless://a612b608-291f-4add-ae78-ece0c450691a@edge.example.test:443?security=tls&type=tcp&sni=edge.example.test"

func TestRenderRoundTripAndPackage(t *testing.T) {
	lines := []Resolved{{Name: "HL-panel", URI: testURI}, {Name: "香港: \"线路\"", URI: "socks5://user:p%40ss@[::1]:1080"}, {Name: "offline", Error: "unavailable"}}
	txt, err := base64.StdEncoding.DecodeString(TXT(lines))
	if err != nil || len(strings.Split(string(txt), "\n")) != 2 {
		t.Fatal("TXT did not preserve two available lines")
	}
	body, err := YAML(lines)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"name":"HL-panel 2"`)) || !bytes.Contains(body, []byte(`"password":"p@ss"`)) || bytes.Contains(body, []byte("offline")) {
		t.Fatal("unsafe group name or malformed YAML")
	}
	// Each generated proxy is JSON flow YAML; decode independently to check escaping.
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "  - ") {
			var value map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "  - ")), &value); err != nil {
				t.Fatal(err)
			}
		}
	}
	r := Record{Token: "sub_" + strings.Repeat("A", 43)}
	pack, err := Package(r, lines, "https://panel.example.test")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(pack), int64(len(pack)))
	if err != nil || len(z.File) != 4 {
		t.Fatal("invalid import ZIP")
	}
	for _, f := range z.File {
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
		case "Shadowrocket-QR.png":
			if _, e = png.Decode(bytes.NewReader(raw)); e != nil {
				t.Fatal(e)
			}
		case "v2rayN-Shadowrocket.txt":
			if string(raw) != "https://panel.example.test"+Paths(r.Token).TXT+"\n" {
				t.Fatal("wrong feed address")
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
