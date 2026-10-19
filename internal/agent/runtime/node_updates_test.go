package runtime

import (
	"encoding/json"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"net"
	"os"
	goruntime "runtime"
	"testing"
)

func TestUpdateVerificationRequiresOwnedTCPAndUDPListeners(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("Linux proc socket ownership")
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	config, _ := json.Marshal(map[string]any{"services": []any{map[string]any{"addr": tcp.Addr().String(), "listener": map[string]string{"type": "tcp"}}, map[string]any{"addr": udp.LocalAddr().String(), "listener": map[string]string{"type": "udp"}}}})
	f := agentv1.ConfigurationFragment{Engine: agentv1.EngineGOST, Config: config}
	if err := verifyFragmentSockets(os.Getpid(), f); err != nil {
		t.Fatal(err)
	}
	if err := verifyFragmentSockets(1, f); err == nil {
		t.Fatal("unowned listeners accepted")
	}
	udp.Close()
	if err := verifyFragmentSockets(os.Getpid(), f); err == nil {
		t.Fatal("missing UDP listener accepted")
	}
}
