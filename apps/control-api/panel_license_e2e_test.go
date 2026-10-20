package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/usagemigration/migratecmd"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"github.com/hongle/hl-panel/internal/panellicenseissuer"
	"github.com/jackc/pgx/v5"
)

type panelE2EDNS struct {
	mu      sync.RWMutex
	records map[string][]string
}

func (d *panelE2EDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]string{}, d.records[name]...), nil
}

// TestPanelLicenseRuntimeLifecycle uses a real control-api child process, two
// fresh PostgreSQL databases and the delivered HT handler over trusted HTTPS.
// DNS and the HT administrator session are synthetic; no production is loaded.
func TestPanelLicenseRuntimeLifecycle(t *testing.T) {
	if os.Getenv("HL_LICENSE_PANEL_E2E") != "true" {
		t.Skip("explicit isolated runtime acceptance required")
	}
	ctx := context.Background()
	config, err := pgx.ParseConfig(os.Getenv("HL_LICENSE_TEST_DATABASE_URL"))
	if err != nil || config.Database != "postgres" || config.Host != "127.0.0.1" {
		t.Fatal("fresh loopback PostgreSQL maintenance database required")
	}
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("connect isolated database:", err)
	}
	t.Cleanup(func() { admin.Close(ctx) })
	newDB := func(suffix string) string {
		name := fmt.Sprintf("hl_panel_e2e_%d_%s", time.Now().UnixNano(), suffix)
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Error(err)
			}
		})
		// pgx.Config.ConnString returns the original input, even after changing
		// Config.Database. Build the new URL explicitly so the panel and issuer
		// cannot accidentally share the maintenance database.
		connectionURL, err := url.Parse(config.ConnString())
		if err != nil || connectionURL.Scheme != "postgres" {
			t.Fatal("isolated PostgreSQL URL required")
		}
		connectionURL.Path = "/" + name
		return connectionURL.String()
	}
	issuerDSN, panelDSN := newDB("issuer"), newDB("panel")
	store, err := panellicenseissuer.OpenPostgres(ctx, issuerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var tokenBytes [48]byte
	if _, err = rand.Read(tokenBytes[:]); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	dns := &panelE2EDNS{records: make(map[string][]string)}
	issuer, err := panellicenseissuer.New(store, panellicenseissuer.Config{Issuer: "https://issuer.example.test", KeyID: "isolated-e2e", SigningKey: key, AdminToken: token, Resolver: dns})
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(issuer.Handler())
	t.Cleanup(public.Close)
	https := httptest.NewTLSServer(issuer.Handler())
	t.Cleanup(https.Close)
	directory := t.TempDir()
	// The installer applies and verifies accounting migrations before starting
	// the API. Exercise the same command path against this fresh panel database.
	dsnFile := filepath.Join(directory, "database-url")
	if err := os.WriteFile(dsnFile, []byte(panelDSN), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"apply", "verify"} {
		if code := migratecmd.Run(ctx, []string{action, "-dsn-file", dsnFile}, io.Discard, io.Discard); code != 0 {
			t.Fatalf("isolated usage migration %s failed (exit %d)", action, code)
		}
	}
	caFile, tokenFile := filepath.Join(directory, "ca.pem"), filepath.Join(directory, "management-token")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: https.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	environment := func(extra map[string]string) []string {
		var env []string
		for _, value := range os.Environ() {
			name, _, _ := strings.Cut(value, "=")
			if strings.HasPrefix(name, "CONTROL_") || strings.HasPrefix(name, "HT_HL_LICENSE_") || strings.HasPrefix(name, "HL_LICENSE_ISSUER_") || name == "SSL_CERT_FILE" {
				continue
			}
			if _, ok := extra[name]; !ok {
				env = append(env, value)
			}
		}
		for name, value := range extra {
			env = append(env, name+"="+value)
		}
		return env
	}
	ht := func(method, path string, body any) map[string]json.RawMessage {
		t.Helper()
		input, _ := json.Marshal(map[string]any{"method": method, "path": path, "body": body, "key": fmt.Sprintf("panel-e2e-%d", time.Now().UnixNano())})
		command := exec.Command(os.Getenv("HL_LICENSE_PANEL_E2E_PYTHON"), filepath.Join(repo, "tests/license-e2e/ht_action.py"))
		command.Env = environment(map[string]string{"SSL_CERT_FILE": caFile, "HT_HL_LICENSE_ISSUER_URL": https.URL, "HT_HL_LICENSE_ISSUER_TOKEN_FILE": tokenFile})
		command.Stdin = bytes.NewReader(input)
		var diagnostics bytes.Buffer
		command.Stderr = &diagnostics
		output, err := command.Output()
		var response struct {
			Status int                        `json:"status"`
			Data   map[string]json.RawMessage `json:"data"`
		}
		if err != nil || json.Unmarshal(output, &response) != nil || response.Status < 200 || response.Status >= 300 {
			var safeCode string
			_ = json.Unmarshal(response.Data["code"], &safeCode)
			t.Logf("isolated HT error code: %q; exception class: %q", safeCode, diagnostics.String())
			t.Fatalf("HT %s %s failed (status %d); sensitive response omitted", method, path, response.Status)
		}
		return response.Data
	}
	read := func(data map[string]json.RawMessage, name string, target any) {
		t.Helper()
		if err := json.Unmarshal(data[name], target); err != nil {
			t.Fatal("invalid field", name)
		}
	}
	// Build the actual executable with disposable pinned trust, never a release.
	keys, _ := json.Marshal(map[string]string{"isolated-e2e": base64.RawURLEncoding.EncodeToString(pub)})
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	binary := filepath.Join(directory, "control-api"+suffix)
	flags := "-X main.panelLicenseIssuerURL=" + public.URL + " -X main.panelLicenseIssuerID=https://issuer.example.test -X main.panelLicensePublicKeysB64=" + base64.RawURLEncoding.EncodeToString(keys)
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"+suffix), "build", "-ldflags", flags, "-o", binary, "./apps/control-api")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build control-api: %v: %s", err, output)
	}
	state := filepath.Join(directory, "identity")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	identity := []byte(`{"domain":"runtime.example.test","instance_id":"hlp-isolated-runtime-e2e"}`)
	if err := os.WriteFile(filepath.Join(state, "identity.json"), identity, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	password := "isolated-panel-acceptance-only"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	panelEnv := environment(map[string]string{"CONTROL_LISTEN_ADDRESS": address, "CONTROL_DATABASE_URL": panelDSN, "CONTROL_BOOTSTRAP_ADMIN_USERNAME": "license-acceptance-admin", "CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH": string(hash), "CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64": base64.StdEncoding.EncodeToString(tokenBytes[:32]), "CONTROL_LICENSE_STATE_DIR": state, "CONTROL_PANEL_LOG_FILE": filepath.Join(directory, "panel.log"), "CONTROL_PANEL_CONTROL_STATE_FILE": filepath.Join(directory, "control-state.json")})
	client := &http.Client{Timeout: 15 * time.Second}
	var session string
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		request, err := http.NewRequest(method, "http://"+address+"/api/v1"+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+session)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", fmt.Sprintf("panel-e2e-%d", time.Now().UnixNano()))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("panel request failed:", err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s status %d want %d; sensitive response omitted", method, path, response.StatusCode, want)
		}
		return raw
	}
	var process *exec.Cmd
	stop := func() {
		if process != nil {
			_ = process.Process.Kill()
			_ = process.Wait()
			process = nil
		}
	}
	t.Cleanup(stop)
	start := func() {
		t.Helper()
		process = exec.Command(binary)
		process.Dir = directory
		process.Env = panelEnv
		log, err := os.OpenFile(filepath.Join(directory, "child.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		process.Stdout = log
		process.Stderr = log
		if err := process.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		log.Close()
		ready := false
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				connection.Close()
				ready = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !ready {
			if raw, err := os.ReadFile(filepath.Join(directory, "child.log")); err == nil {
				for _, line := range strings.Split(string(raw), "\n") {
					var entry map[string]any
					if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "control api stopped" {
						detail := fmt.Sprint(entry["error"])
						for _, secret := range []string{token, panelDSN, issuerDSN, password, string(hash)} {
							detail = strings.ReplaceAll(detail, secret, "[redacted]")
						}
						t.Log("isolated child startup error:", detail)
					}
				}
			}
			t.Fatal("control-api readiness failed; inspect isolated child.log")
		}
		var result auth.LoginResult
		if err := json.Unmarshal(call("POST", "/auth/login", map[string]string{"username": "license-acceptance-admin", "password": password}, 200), &result); err != nil {
			t.Fatal(err)
		}
		session = result.AccessToken
	}
	status := func() p.Status {
		t.Helper()
		var s p.Status
		if err := json.Unmarshal(call("GET", "/license/status", nil, 200), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	start()
	if s := status(); s.Status != "unactivated" || s.ManagementAllowed {
		t.Fatal("unactivated runtime did not restrict writes")
	}
	call("POST", "/user-groups", map[string]any{"name": "blocked before activation"}, 403)
	expires := time.Now().UTC().Add(35 * time.Second)
	issued := ht("POST", "", map[string]any{"domain": "runtime.example.test", "term": p.Term{Kind: "until", ExpiresAt: &expires}, "reason": "真实运行时隔离验收"})
	var record p.LicenseRecord
	var code string
	read(issued, "license", &record)
	read(issued, "code", &code)
	var challenge p.Challenge
	if err := json.Unmarshal(call("POST", "/license/activation/prepare", map[string]string{"code": code}, 200), &challenge); err != nil {
		t.Fatal(err)
	}
	code = ""
	call("POST", "/license/activation/commit", map[string]any{}, 502)
	if status().ManagementAllowed {
		t.Fatal("missing DNS granted management")
	}
	dns.mu.Lock()
	dns.records[challenge.DNSName] = []string{challenge.DNSValue}
	dns.mu.Unlock()
	call("POST", "/license/activation/commit", map[string]any{}, 200)
	if s := status(); !s.ManagementAllowed || s.LicenseID != record.ID || !s.ReminderDue {
		t.Fatal("activation or near-expiry reminder failed")
	}
	call("POST", "/license/reminders/dismiss", map[string]any{}, 200)
	if status().ReminderDue {
		t.Fatal("daily reminder dismissal failed")
	}
	call("POST", "/user-groups", map[string]any{"name": "保留的隔离客户组", "allow_direct": true}, 201)
	before := call("GET", "/user-groups", nil, 200)
	keyBefore, err := os.ReadFile(filepath.Join(state, "instance-key"))
	if err != nil {
		t.Fatal(err)
	}
	stop()
	start()
	if s := status(); !s.ManagementAllowed || s.ReminderDue {
		t.Fatal("restart lost authorization or daily dismissal")
	}
	if !bytes.Equal(before, call("GET", "/user-groups", nil, 200)) {
		t.Fatal("restart changed durable business data")
	}
	t.Log("real HT handler -> HTTPS issuer -> PostgreSQL -> actual panel activation and restart passed")
	for time.Now().Before(expires.Add(150 * time.Millisecond)) {
		time.Sleep(100 * time.Millisecond)
	}
	if s := status(); s.Status != "expired" || s.ManagementAllowed {
		t.Fatal("commercial expiry did not restrict runtime")
	}
	call("POST", "/user-groups", map[string]any{"name": "blocked after expiry"}, 403)
	if !bytes.Equal(before, call("GET", "/user-groups", nil, 200)) {
		t.Fatal("expiry changed business data")
	}
	result := ht("GET", "", nil)
	var items []p.LicenseRecord
	read(result, "items", &items)
	for _, item := range items {
		if item.ID == record.ID {
			record = item
		}
	}
	renewed := ht("POST", "/"+record.ID+"/extend", map[string]any{"expected_revision": record.Revision, "term": p.Term{Kind: "days", Days: 30}, "reason": "到期恢复隔离验收"})
	read(renewed, "license", &record)
	// Restart also exercises reading an expired signed cache. Runtime background
	// synchronization must restore permission without another activation code.
	stop()
	start()
	for deadline := time.Now().Add(20 * time.Second); !status().ManagementAllowed; {
		if time.Now().After(deadline) {
			t.Fatal("automatic renewal sync did not restore management")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s := status(); s.LicenseID != record.ID || s.ExpiresAt == nil || !s.ExpiresAt.Equal(*record.LicenseExpiresAt) {
		t.Fatal("latest HT commercial expiry not followed")
	}
	if keyAfter, err := os.ReadFile(filepath.Join(state, "instance-key")); err != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("renewal replaced instance key")
	}
	if !bytes.Equal(before, call("GET", "/user-groups", nil, 200)) {
		t.Fatal("renewal changed stored business data")
	}
	call("POST", "/user-groups", map[string]any{"name": "续期后可管理", "allow_direct": true}, 201)
	t.Log("actual expiry -> write restriction -> HT renewal -> automatic restore passed; instance key and business data preserved")
	// Optional bounded browser review uses this actual runtime, not API fixtures.
	if marker := os.Getenv("HL_LICENSE_PANEL_E2E_BROWSER_MARKER"); marker != "" {
		metadata, _ := json.Marshal(map[string]string{"api_url": "http://" + address, "username": "license-acceptance-admin", "password": password})
		if err := os.WriteFile(marker, metadata, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("real runtime ready for bounded browser review")
		for deadline := time.Now().Add(10 * time.Minute); ; {
			if _, err := os.Stat(marker + ".done"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("browser review deadline exceeded")
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	ht("POST", "/"+record.ID+"/revoke", map[string]any{"expected_revision": record.Revision, "reason": "撤销隔离验收"})
	stop()
	start()
	for deadline := time.Now().Add(20 * time.Second); status().Status != "revoked"; {
		if time.Now().After(deadline) {
			t.Fatal("revocation not followed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	call("POST", "/user-groups", map[string]any{"name": "blocked after revocation"}, 403)
	t.Log("HT revocation followed; reads/login retained and writes restricted")
}
