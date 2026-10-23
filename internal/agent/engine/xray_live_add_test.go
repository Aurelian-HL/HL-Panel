package engine

import (
	"context"
	"crypto/rand"
	stdbinary "encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/vlessconfig"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

func liveAddFragment(extra bool) json.RawMessage {
	in := []any{map[string]any{"tag": "old-in", "protocol": "socks", "port": 18080}}
	out := []any{map[string]any{"tag": "old-out", "protocol": "freedom"}}
	rules := []any{map[string]any{"type": "field", "inboundTag": []string{"old-in"}, "outboundTag": "old-out"}}
	if extra {
		in = append(in, map[string]any{"tag": "new-in", "protocol": "socks", "port": 18081})
		out = append(out, map[string]any{"tag": "new-out", "protocol": "blackhole"})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"new-in"}, "outboundTag": "new-out"})
	}
	data, _ := json.Marshal(map[string]any{"inbounds": in, "outbounds": out, "routing": map[string]any{"rules": rules}})
	return data
}

func liveAddPrepared(t *testing.T, a *XrayProcessAdapter, generation agentv1.NodeConfigGeneration, extra bool) PreparedConfiguration {
	t.Helper()
	p, err := a.Prepare(context.Background(), xrayDesired(t, generation, []agentv1.ConfigurationFragment{{
		GroupID: "rule", GroupRevision: agentv1.GroupRevision(generation), Engine: agentv1.EngineXray, Config: liveAddFragment(extra),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestXrayAdditionsRejectChangesToExistingTraffic(t *testing.T) {
	a := newTestXrayAdapter(t, &recordingXrayRunner{}, false)
	old, next := liveAddPrepared(t, a, 1, false), liveAddPrepared(t, a, 2, true)
	before, _ := os.ReadFile(old.adapterPath)
	after, _ := os.ReadFile(next.adapterPath)
	if _, ok := planXrayAdditions(before, after); !ok {
		t.Fatal("disjoint addition rejected")
	}
	cases := map[string]func(map[string]any){
		"edit inbound":   func(m map[string]any) { m["inbounds"].([]any)[0].(map[string]any)["port"] = 19000 },
		"remove inbound": func(m map[string]any) { m["inbounds"] = m["inbounds"].([]any)[1:] },
		"edit outbound":  func(m map[string]any) { m["outbounds"].([]any)[0].(map[string]any)["protocol"] = "blackhole" },
		"duplicate tag":  func(m map[string]any) { m["inbounds"].([]any)[1].(map[string]any)["tag"] = "old-in" },
		"catch all route": func(m map[string]any) {
			delete(m["routing"].(map[string]any)["rules"].([]any)[2].(map[string]any), "inboundTag")
		},
		"route old listener": func(m map[string]any) {
			m["routing"].(map[string]any)["rules"].([]any)[2].(map[string]any)["inboundTag"] = []string{"new-in", "old-in"}
		},
		"reorder old routes": func(m map[string]any) { v := m["routing"].(map[string]any)["rules"].([]any); v[0], v[1] = v[1], v[0] },
		"global policy":      func(m map[string]any) { m["policy"] = map[string]any{"system": map[string]any{}} },
		"routing settings":   func(m map[string]any) { m["routing"].(map[string]any)["domainStrategy"] = "IPIfNonMatch" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			_ = json.Unmarshal(after, &doc)
			change(doc)
			data, _ := json.Marshal(doc)
			if _, ok := planXrayAdditions(before, data); ok {
				t.Fatal("unsafe update accepted")
			}
		})
	}
	var doc map[string]any
	_ = json.Unmarshal(before, &doc)
	doc["api"].(map[string]any)["services"] = []string{"StatsService"}
	legacy, _ := json.Marshal(doc)
	if _, ok := planXrayAdditions(legacy, after); ok {
		t.Fatal("legacy process lacks required API")
	}
}

func orderedLiveRule(id, inbound, outbound string, port int) agentv1.ConfigurationFragment {
	config, _ := json.Marshal(map[string]any{
		"inbounds":  []any{map[string]any{"tag": inbound, "protocol": "socks", "port": port}},
		"outbounds": []any{map[string]any{"tag": outbound, "protocol": "freedom"}},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{inbound}, "outboundTag": outbound},
		}},
	})
	return agentv1.ConfigurationFragment{GroupID: id, GroupRevision: 1, Engine: agentv1.EngineXray, Config: config}
}

func orderedLivePrepared(t *testing.T, a *XrayProcessAdapter, generation agentv1.NodeConfigGeneration, fragments ...agentv1.ConfigurationFragment) PreparedConfiguration {
	t.Helper()
	prepared, err := a.Prepare(context.Background(), xrayDesired(t, generation, fragments))
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestXrayLeadingOutboundAdditionRequiresFallbackForEveryInbound(t *testing.T) {
	a := newTestXrayAdapter(t, &recordingXrayRunner{}, false)
	old := orderedLiveRule("z-old", "old-in", "old-out", 18080)
	newRule := orderedLiveRule("a-new", "new-in", "new-out", 18081)
	before, _ := os.ReadFile(orderedLivePrepared(t, a, 1, old).adapterPath)
	after, _ := os.ReadFile(orderedLivePrepared(t, a, 2, newRule, old).adapterPath)
	if _, ok := planXrayAdditions(before, after); !ok {
		t.Fatal("new leading rule with explicit fallback routes should be eligible")
	}
	removeFallback := func(data []byte, tag string) []byte {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		routing := doc["routing"].(map[string]any)
		rules := routing["rules"].([]any)
		kept := make([]any, 0, len(rules))
		for _, entry := range rules {
			rule := entry.(map[string]any)
			inboundTags := rule["inboundTag"].([]any)
			if len(inboundTags) == 1 && inboundTags[0] == tag {
				continue
			}
			kept = append(kept, entry)
		}
		routing["rules"] = kept
		encoded, _ := json.Marshal(doc)
		return encoded
	}
	for _, tag := range []string{"old-in", "hl-stats-api-in"} {
		if _, ok := planXrayAdditions(removeFallback(before, tag), removeFallback(after, tag)); ok {
			t.Fatalf("accepted changed default without fallback for %s", tag)
		}
	}
	if _, ok := planXrayAdditions(before, removeFallback(after, "new-in")); ok {
		t.Fatal("accepted changed default without fallback for new inbound")
	}
	var conditional map[string]any
	_ = json.Unmarshal(after, &conditional)
	conditional["routing"].(map[string]any)["rules"].([]any)[1].(map[string]any)["domain"] = []string{"example.com"}
	encoded, _ := json.Marshal(conditional)
	if _, ok := planXrayAdditions(before, encoded); ok {
		t.Fatal("accepted conditional route as an unconditional fallback")
	}
}

func TestXrayRepeatedLeadingRuleAdditionAndRollbackKeepProcess(t *testing.T) {
	r := &liveAPITestRunner{tags: map[string]bool{"old-in": true}}
	a := newTestXrayAdapter(t, r, true)
	old := orderedLiveRule("z-old", "old-in", "old-out", 18080)
	second := orderedLiveRule("m-second", "second-in", "second-out", 18081)
	third := orderedLiveRule("a-third", "third-in", "third-out", 18082)
	firstConfig := orderedLivePrepared(t, a, 1, old)
	secondConfig := orderedLivePrepared(t, a, 2, second, old)
	thirdConfig := orderedLivePrepared(t, a, 3, third, second, old)
	if err := a.Commit(context.Background(), firstConfig); err != nil {
		t.Fatal(err)
	}
	epoch := a.CounterEpoch()
	for _, config := range []PreparedConfiguration{secondConfig, thirdConfig} {
		if err := a.Commit(context.Background(), config); err != nil {
			t.Fatal(err)
		}
		if err := a.Verify(context.Background(), config); err != nil {
			t.Fatal(err)
		}
	}
	for _, config := range []PreparedConfiguration{secondConfig, firstConfig} {
		if err := a.Rollback(context.Background(), &config); err != nil {
			t.Fatal(err)
		}
		if err := a.Verify(context.Background(), config); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.starts) != 1 || r.processes[0].stopCount != 0 || a.CounterEpoch() != epoch {
		t.Fatal("repeated leading additions or rollback restarted the process")
	}
	if !r.tags["old-in"] || r.tags["second-in"] || r.tags["third-in"] {
		t.Fatal("rollback did not restore only the original listener")
	}
}

func TestXrayRealLeadingRealityUDPRulePreservesOpenConnection(t *testing.T) {
	binary := os.Getenv("NYVP_XRAY_BINARY")
	if binary == "" {
		t.Skip("set NYVP_XRAY_BINARY for isolated loopback Xray integration test")
	}
	publicKey, privateKey, err := vless.GenerateRealityKeyPair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	decoy := httptest.NewUnstartedServer(http.NotFoundHandler())
	decoy.Config.ErrorLog = log.New(io.Discard, "", 0)
	decoy.StartTLS()
	defer decoy.Close()
	decoyURL, err := url.Parse(decoy.URL)
	if err != nil {
		t.Fatal(err)
	}
	udpEcho, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close()
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, address, err := udpEcho.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = udpEcho.WriteToUDP(buffer[:n], address)
		}
	}()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	freePort := func() int {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		return port
	}
	oldPort, newPort, apiPort, oldClientPort, udpClientPort := freePort(), freePort(), freePort(), freePort(), freePort()
	root := t.TempDir()
	a, err := NewXrayProcessAdapter(XrayAdapterOptions{
		StagingDir:      filepath.Join(root, "staging"),
		ActivePath:      filepath.Join(root, "active", "config.json"),
		BinaryPath:      binary,
		AutoStart:       true,
		StatsAPIAddress: net.JoinHostPort("127.0.0.1", fmt.Sprint(apiPort)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(context.Background()) }()
	const userID = "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d"
	oldConfig, err := vlessconfig.CompileDirect(forwarding.Rule{
		ID: "old-tcp", CustomerID: "test-customer", EntryGroupID: "test-entry",
		IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP,
		EgressMode: forwarding.EgressDirect, VLESSOutboundMode: forwarding.VLESSOutboundDirect,
		VLESSFlow: "xtls-rprx-vision", VLESSFlowExplicit: true,
		Targets: []forwarding.Target{{Host: "127.0.0.1", Port: echo.Addr().(*net.TCPAddr).Port}},
	}, vlessconfig.Identity{UUID: userID}, vlessconfig.RealityProfile{
		ServerName: "localhost", Dest: decoyURL.Host, PrivateKey: privateKey,
		ShortIDs: []string{"01234567"}, Flow: "xtls-rprx-vision",
	}, vlessconfig.Listener{Address: "127.0.0.1", Port: oldPort})
	if err != nil {
		t.Fatal(err)
	}
	old := agentv1.ConfigurationFragment{GroupID: "z-old", GroupRevision: 1, Engine: agentv1.EngineXray, Config: oldConfig}
	newConfig, err := vlessconfig.CompileDirect(forwarding.Rule{
		ID: "new-udp", CustomerID: "test-customer", EntryGroupID: "test-entry",
		IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolUDP,
		EgressMode: forwarding.EgressDirect, VLESSOutboundMode: forwarding.VLESSOutboundDirect,
		VLESSFlow: "", VLESSFlowExplicit: true,
		Targets: []forwarding.Target{{Host: "127.0.0.1", Port: udpEcho.LocalAddr().(*net.UDPAddr).Port}},
	}, vlessconfig.Identity{UUID: userID}, vlessconfig.RealityProfile{
		ServerName: "localhost", Dest: decoyURL.Host, PrivateKey: privateKey, ShortIDs: []string{"01234567"},
	}, vlessconfig.Listener{Address: "127.0.0.1", Port: newPort})
	if err != nil {
		t.Fatal(err)
	}
	newRule := agentv1.ConfigurationFragment{GroupID: "a-new", GroupRevision: 1, Engine: agentv1.EngineXray, Config: newConfig}
	first := orderedLivePrepared(t, a, 1, old)
	second := orderedLivePrepared(t, a, 2, newRule, old)
	before, err := os.ReadFile(first.adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(second.adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := planXrayAdditions(before, after); !ok {
		t.Fatal("leading VLESS Reality UDP rule should be eligible for live addition")
	}
	for _, config := range []PreparedConfiguration{first, second} {
		if err := a.Validate(context.Background(), config); err != nil {
			t.Fatalf("native Xray validation: %v", err)
		}
	}
	if err := a.CommitValidated(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	pid := a.process.(*execXrayProcess).PID()
	epoch := a.CounterEpoch()
	dial := func(port int) net.Conn {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 200*time.Millisecond)
			if err == nil {
				return conn
			}
			if time.Now().After(deadline) {
				t.Fatalf("listener %d unavailable: %v", port, err)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	exchange := func(conn net.Conn, payload string) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatal(err)
		}
		if string(response) != payload {
			t.Fatalf("echo = %q, want %q", response, payload)
		}
	}
	clientRunner, err := NewExecXrayRunner(binary)
	if err != nil {
		t.Fatal(err)
	}
	startClient := func(localPort, serverPort int, flow string, udp bool) {
		t.Helper()
		user := map[string]any{"id": userID, "encryption": "none"}
		if flow != "" {
			user["flow"] = flow
		}
		clientConfig, err := json.Marshal(map[string]any{
			"log": map[string]any{"loglevel": "warning", "access": "none"},
			"inbounds": []any{map[string]any{
				"listen": "127.0.0.1", "port": localPort, "protocol": "socks",
				"settings": map[string]any{"auth": "noauth", "udp": udp},
			}},
			"outbounds": []any{map[string]any{
				"protocol": "vless",
				"settings": map[string]any{"vnext": []any{map[string]any{
					"address": "127.0.0.1", "port": serverPort, "users": []any{user},
				}}},
				"streamSettings": map[string]any{
					"network": "tcp", "security": "reality",
					"realitySettings": map[string]any{
						"serverName": "localhost", "publicKey": publicKey,
						"shortId": "01234567", "fingerprint": "chrome", "spiderX": "/",
					},
				},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		clientPath := filepath.Join(root, fmt.Sprintf("client-%d.json", localPort))
		if err := os.WriteFile(clientPath, clientConfig, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := clientRunner.Test(context.Background(), clientPath); err != nil {
			t.Fatal(err)
		}
		clientProcess, err := clientRunner.Start(context.Background(), clientPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = clientProcess.Stop(context.Background()) })
	}
	socksRequest := func(localPort int, command byte, targetPort int) (net.Conn, []byte) {
		t.Helper()
		control := dial(localPort)
		_ = control.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := control.Write([]byte{5, 1, 0}); err != nil {
			t.Fatal(err)
		}
		method := make([]byte, 2)
		if _, err := io.ReadFull(control, method); err != nil || method[0] != 5 || method[1] != 0 {
			t.Fatalf("SOCKS5 greeting failed: %v", err)
		}
		address := []byte{0, 0, 0, 0}
		if command == 1 {
			address = []byte{127, 0, 0, 1}
		}
		request := append([]byte{5, command, 0, 1}, address...)
		request = append(request, byte(targetPort>>8), byte(targetPort))
		if _, err := control.Write(request); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 10)
		if _, err := io.ReadFull(control, response); err != nil || response[0] != 5 || response[1] != 0 || response[3] != 1 {
			t.Fatalf("SOCKS5 command %d failed: %v", command, err)
		}
		return control, response
	}
	startClient(oldClientPort, oldPort, "xtls-rprx-vision", false)
	oldConn, _ := socksRequest(oldClientPort, 1, echo.Addr().(*net.TCPAddr).Port)
	defer oldConn.Close()
	exchange(oldConn, "before")
	if err := a.CommitValidated(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if a.process.(*execXrayProcess).PID() != pid || a.CounterEpoch() != epoch {
		t.Fatal("new leading rule restarted Xray")
	}
	exchange(oldConn, "after")
	startClient(udpClientPort, newPort, "", true)
	control, response := socksRequest(udpClientPort, 3, 0)
	defer control.Close()
	relayIP := net.IP(response[4:8])
	if relayIP.IsUnspecified() {
		relayIP = net.ParseIP("127.0.0.1")
	}
	udpClient, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: relayIP, Port: int(stdbinary.BigEndian.Uint16(response[8:10]))})
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()
	targetPort := udpEcho.LocalAddr().(*net.UDPAddr).Port
	payload := []byte("vless-reality-direct-udp")
	packet := append([]byte{0, 0, 0, 1, 127, 0, 0, 1, byte(targetPort >> 8), byte(targetPort)}, payload...)
	if _, err := udpClient.Write(packet); err != nil {
		t.Fatal(err)
	}
	_ = udpClient.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, 2048)
	n, err := udpClient.Read(buffer)
	if err != nil || n < 10 || string(buffer[10:n]) != string(payload) {
		t.Fatalf("VLESS Reality UDP echo failed: bytes=%d error=%v", n, err)
	}
	exchange(oldConn, "udp-active")
	// Ordinary desired-config deletion must use the live path as well; testing
	// rollback alone previously hid the restart on customer rule deletion.
	if err := a.CommitValidated(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if a.process.(*execXrayProcess).PID() != pid || a.CounterEpoch() != epoch {
		t.Fatal("deleting Reality UDP rule restarted Xray")
	}
	exchange(oldConn, "rule-deleted")
	if err := a.CommitValidated(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := a.Rollback(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if a.process.(*execXrayProcess).PID() != pid || a.CounterEpoch() != epoch {
		t.Fatal("rollback restarted Xray")
	}
	exchange(oldConn, "rollback")
}

type liveAPITestRunner struct {
	recordingXrayRunner
	tags            map[string]bool
	outTags         map[string]bool
	calls           []string
	fail            string
	uncertain       bool
	badCompensation bool
	afterAdd        func()
}

func (r *liveAPITestRunner) API(_ context.Context, _ string, operation string, doc []byte) ([]byte, error) {
	r.calls = append(r.calls, operation)
	var document map[string][]json.RawMessage
	if operation == "adi" || operation == "rmi" || operation == "ado" || operation == "rmo" {
		_ = json.Unmarshal(doc, &document)
	}
	if operation == "ado" || operation == "rmo" {
		if r.outTags == nil {
			r.outTags = map[string]bool{}
		}
		for _, out := range document["outbounds"] {
			if operation == "ado" {
				r.outTags[xrayEntryTag(out)] = true
			} else {
				delete(r.outTags, xrayEntryTag(out))
			}
		}
	}
	if operation == "adi" {
		for _, in := range document["inbounds"] {
			r.tags[xrayEntryTag(in)] = true
		}
		if r.afterAdd != nil {
			r.afterAdd()
		}
	}
	if operation == "rmi" {
		for _, in := range document["inbounds"] {
			delete(r.tags, xrayEntryTag(in))
		}
	}
	if r.fail == operation {
		r.fail = ""
		if r.uncertain {
			return nil, errXrayAPIUncertain
		}
		return nil, errors.New("secret-not-for-logs")
	}
	if operation == "lsi" || operation == "lso" {
		if r.badCompensation {
			return []byte(`{}`), nil
		}
		key, tags := "inbounds", r.tags
		if operation == "lso" {
			key, tags = "outbounds", r.outTags
		}
		in := []map[string]string{}
		for tag := range tags {
			in = append(in, map[string]string{"tag": tag})
		}
		return json.Marshal(map[string]any{key: in})
	}
	return nil, nil
}

func TestXrayLiveAdditionAndTransactionRollbackKeepProcess(t *testing.T) {
	r := &liveAPITestRunner{tags: map[string]bool{"old-in": true}}
	a := newTestXrayAdapter(t, r, true)
	first := liveAddPrepared(t, a, 1, false)
	if err := a.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	epoch := a.CounterEpoch()
	second := liveAddPrepared(t, a, 2, true)
	if err := a.Commit(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.calls, ",") != "ado,adrules,adi" {
		t.Fatal("incorrect API operation order")
	}
	if len(r.starts) != 1 || r.processes[0].stopCount != 0 || a.CounterEpoch() != epoch {
		t.Fatal("addition restarted old process")
	}
	if err := a.Rollback(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if r.tags["new-in"] || !r.tags["old-in"] || len(r.starts) != 1 || a.CounterEpoch() != epoch {
		t.Fatal("rollback changed previous runtime")
	}
}

func TestXrayLiveFailureRestoresPreviousConfiguration(t *testing.T) {
	for _, stage := range []string{"ado", "adrules", "adi", "uncertain", "bad compensation", "disk"} {
		t.Run(stage, func(t *testing.T) {
			r := &liveAPITestRunner{tags: map[string]bool{"old-in": true}}
			a := newTestXrayAdapter(t, r, true)
			first := liveAddPrepared(t, a, 1, false)
			if err := a.Commit(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			second := liveAddPrepared(t, a, 2, true)
			r.fail = stage
			if stage == "uncertain" {
				r.fail = "adi"
				r.uncertain = true
			}
			if stage == "bad compensation" {
				r.fail = "adi"
				r.badCompensation = true
			}
			if stage == "disk" {
				// Force the persistence boundary to fail after the RPC succeeds.
				r.afterAdd = func() { a.activePath = filepath.Join(a.activePath, "unwritable") }
			}
			originalPath := a.activePath
			err := a.Commit(context.Background(), second)
			if stage == "disk" {
				a.activePath = originalPath
			}
			if !errors.Is(err, ErrXrayLiveUpdate) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error = %v", err)
			}
			blocked := stage == "uncertain" || stage == "bad compensation" || stage == "disk"
			if verifyErr := a.Verify(context.Background(), first); (verifyErr != nil) != blocked || blocked && !errors.Is(verifyErr, ErrLiveStateUncertain) {
				t.Fatalf("verify=%v blocked=%v", verifyErr, blocked)
			}
			if len(r.starts) != 1 || r.processes[0].stopCount != 0 {
				t.Fatal("failed addition restarted the process")
			}
			if !blocked && (r.tags["new-in"] || !r.tags["old-in"]) {
				t.Fatal("partial API addition not compensated")
			}
		})
	}
}
