package xray_test

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/vlessconfig"
	"github.com/hongle/hl-panel/internal/provisioning/vless"
)

// TestVLESSRealityUDPLoopback proves that UDP is the final target network,
// while both customer-facing legs remain VLESS + Reality over TCP. The test
// uses only loopback sockets and a disposable Xray process on each side.
func TestVLESSRealityUDPLoopback(t *testing.T) {
	binaryPath := xrayBinary(t)
	publicKey, privateKey, err := vless.GenerateRealityKeyPair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	udpTarget, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udpTarget.Close() })
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, address, readErr := udpTarget.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			_, _ = udpTarget.WriteToUDP(buffer[:n], address)
		}
	}()

	decoyCert, decoyKey, _ := testCertificate(t)
	decoyCertificate, err := tls.LoadX509KeyPair(decoyCert, decoyKey)
	if err != nil {
		t.Fatal(err)
	}
	decoyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	decoyTLS := tls.NewListener(decoyListener, &tls.Config{Certificates: []tls.Certificate{decoyCertificate}})
	decoyDone := make(chan struct{})
	go func() {
		defer close(decoyDone)
		for {
			connection, acceptErr := decoyTLS.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				_, _ = io.Copy(io.Discard, connection)
				_ = connection.Close()
			}()
		}
	}()
	t.Cleanup(func() {
		_ = decoyTLS.Close()
		<-decoyDone
	})

	frontPort := freeLoopbackPort(t)
	serverProfile := vlessconfig.RealityProfile{
		ServerName: "localhost", Dest: decoyTLS.Addr().String(), PrivateKey: privateKey,
		ShortIDs: []string{"01234567"}, Flow: "",
	}
	rule := forwarding.Rule{
		ID: "udp-reality-loopback", CustomerID: "udp-customer", EntryGroupID: "udp-entry",
		IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolUDP,
		EgressMode: forwarding.EgressDirect, VLESSOutboundMode: forwarding.VLESSOutboundDirect,
		VLESSFlow: "", VLESSFlowExplicit: true,
		Targets: []forwarding.Target{{Host: "127.0.0.1", Port: udpTarget.LocalAddr().(*net.UDPAddr).Port}},
	}
	serverConfig, err := vlessconfig.CompileDirect(rule, vlessconfig.Identity{UUID: "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d"}, serverProfile, vlessconfig.Listener{Address: "127.0.0.1", Port: frontPort})
	if err != nil {
		t.Fatal(err)
	}
	var serverDocument map[string]any
	if err := json.Unmarshal(serverConfig, &serverDocument); err != nil {
		t.Fatal(err)
	}
	serverConfig, err = json.Marshal(serverDocument)
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binaryPath, privateConfig(t, serverConfig), localAddress(frontPort))

	clientProfile := vless.Profile{
		Endpoint: vless.Endpoint{Hostname: "127.0.0.1", Port: frontPort, Name: "udp-reality-loopback"},
		Identity: vless.Identity{UUID: "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d"},
		Reality:  &vless.RealityProfile{ServerName: "localhost", PublicKey: publicKey, Destination: decoyTLS.Addr().String(), ShortID: "01234567", Fingerprint: "chrome", SpiderX: "/"},
	}
	uri, err := vless.URI(clientProfile)
	if err != nil {
		t.Fatal(err)
	}
	clientPort := freeLoopbackPort(t)
	clientConfig := realityNativeClientFromURI(t, uri, clientPort)
	var clientDocument map[string]any
	if err := json.Unmarshal(clientConfig, &clientDocument); err != nil {
		t.Fatal(err)
	}
	clientInbound := clientDocument["inbounds"].([]any)[0].(map[string]any)
	clientInbound["settings"].(map[string]any)["udp"] = true
	clientConfig, err = json.Marshal(clientDocument)
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, binaryPath, privateConfig(t, clientConfig), localAddress(clientPort))

	control, err := net.DialTimeout("tcp", localAddress(clientPort), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if _, err := control.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(control, method); err != nil || method[0] != 5 || method[1] != 0 {
		t.Fatalf("SOCKS5 greeting failed: %v response=%v", err, method)
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	relay, err := readSOCKS5Bind(control)
	if err != nil {
		t.Fatal(err)
	}
	if relay.IP.IsUnspecified() {
		relay.IP = net.ParseIP("127.0.0.1")
	}
	udpClient, err := net.DialUDP("udp4", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()
	payload := []byte("vless-reality-udp-loopback")
	packet := append([]byte{0, 0, 0, 1, 127, 0, 0, 1, byte(udpTarget.LocalAddr().(*net.UDPAddr).Port >> 8), byte(udpTarget.LocalAddr().(*net.UDPAddr).Port)}, payload...)
	if _, err := udpClient.Write(packet); err != nil {
		t.Fatal(err)
	}
	if err := udpClient.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2048)
	n, err := udpClient.Read(response)
	if err != nil {
		t.Fatal(fmt.Errorf("SOCKS5 UDP response: %w", err))
	}
	if n < 10 || response[0] != 0 || response[1] != 0 || response[2] != 0 || response[3] != 1 || string(response[10:n]) != string(payload) {
		t.Fatalf("unexpected SOCKS5 UDP response: %v", response[:n])
	}
}

func readSOCKS5Bind(connection net.Conn) (*net.UDPAddr, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	if header[0] != 5 || header[1] != 0 {
		return nil, fmt.Errorf("SOCKS5 UDP associate rejected: %v", header)
	}
	var host string
	switch header[3] {
	case 1:
		address := make([]byte, 4)
		if _, err := io.ReadFull(connection, address); err != nil {
			return nil, err
		}
		host = net.IP(address).String()
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(connection, length); err != nil {
			return nil, err
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(connection, address); err != nil {
			return nil, err
		}
		host = string(address)
	case 4:
		address := make([]byte, 16)
		if _, err := io.ReadFull(connection, address); err != nil {
			return nil, err
		}
		host = net.IP(address).String()
	default:
		return nil, fmt.Errorf("unknown SOCKS5 address type %d", header[3])
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(connection, port); err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: net.ParseIP(host), Port: int(binary.BigEndian.Uint16(port))}, nil
}
