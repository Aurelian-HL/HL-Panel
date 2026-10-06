package forwarding

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNYTextPreviewReportsRowsAndAutomaticPort(t *testing.T) {
	service := NewService(&captureRepository{}, func() time.Time { return time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC) })
	preview, err := service.PreviewImport(context.Background(), TransferRequest{
		Format:  ImportFormatNYText,
		Content: "主线路##landing.example.test#443\r\n坏行#123\n备用#12001#192.0.2.8#8443",
		Mapping: ImportMapping{CustomerID: "customer-1", EntryGroupID: "entry-1", EgressMode: EgressDirect, Protocol: ProtocolTCP, SelectionPolicy: SelectionRoundRobin},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Total != 3 || preview.Valid != 2 || preview.Invalid != 1 || preview.Rows[0].Request.ListenPort != 0 || preview.Rows[0].Line != 1 || preview.Rows[1].Line != 2 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if preview.Rows[1].Issues[0].Code != "invalid_columns" || preview.CanCommit() {
		t.Fatalf("invalid line was not rejected: %+v", preview.Rows[1])
	}
}

func TestJSONV1PreservesAdvancedFieldsAndRejectsUnknownSchema(t *testing.T) {
	request := validRequest()
	request.Revision = 7
	request.RuleGroupID = "rg-1"
	request.SelectionPolicy = SelectionFailover
	request.AcceptProxyProtocol = true
	request.SendProxyProtocol = SendProxyV2TCP
	request.SpeedLimitMbps = 200
	request.IPLimit = 5
	request.ConnectionLimit = 20
	document := ExportDocument{SchemaVersion: ExportSchemaV1, ExportedAt: time.Now().UTC(), Rules: []ExportRule{{Operation: ImportUpsert, ID: "fwd-existing", Rule: request}}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := NewService(&captureRepository{}, nil).PreviewImport(context.Background(), TransferRequest{Format: ImportFormatJSONV1, Content: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	row := preview.Rows[0]
	if preview.Invalid != 0 || row.Request.SelectionPolicy != SelectionFailover || !row.Request.AcceptProxyProtocol || row.Request.SendProxyProtocol != SendProxyV2TCP || row.Request.SpeedLimitMbps != 200 || row.Request.IPLimit != 5 || row.Request.ConnectionLimit != 20 {
		t.Fatalf("advanced fields were lost: %+v", row)
	}
	bad := strings.Replace(string(raw), ExportSchemaV1, "nyvp.forwarding-rules/v2", 1)
	preview, err = NewService(&captureRepository{}, nil).PreviewImport(context.Background(), TransferRequest{Format: ImportFormatJSONV1, Content: bad})
	if err != nil || len(preview.GlobalIssues) != 1 || preview.GlobalIssues[0].Code != "unsupported_schema" {
		t.Fatalf("unknown schema accepted: %+v %v", preview, err)
	}
}

func TestJSONV1PreservesRealityFields(t *testing.T) {
	request := validRequest()
	request.Revision = 7
	request.IngressProtocol = IngressVLESSReality
	request.Protocol = ProtocolTCP
	request.EgressMode = EgressDirect
	request.VLESSOutboundMode = VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.test"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "landing-user"
	request.VLESSSOCKS5Password = "landing-secret"
	request.VLESSFlow = "xtls-rprx-vision"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = "AbCdEf0123456789AbCdEf0123456789AbCdEf01234"
	request.RealityShortID = "0123456789abcdef"
	document := ExportDocument{SchemaVersion: ExportSchemaV1, ExportedAt: time.Now().UTC(), Rules: []ExportRule{{Operation: ImportUpsert, ID: "fwd-reality", Rule: request}}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := NewService(&captureRepository{}, nil).PreviewImport(context.Background(), TransferRequest{Format: ImportFormatJSONV1, Content: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	row := preview.Rows[0]
	if preview.Invalid != 0 || row.Request.IngressProtocol != IngressVLESSReality || row.Request.VLESSFlow != "xtls-rprx-vision" || row.Request.RealityServerName != "www.example.com" || row.Request.RealityPublicKey == "" || row.Request.RealityShortID != "0123456789abcdef" {
		t.Fatalf("Reality fields were lost: %+v", row)
	}
}

func TestExportProjectionPreservesRealityParameters(t *testing.T) {
	rule := Rule{
		Name: "Reality 规则", CustomerID: "customer-1", EntryGroupID: "entry-1", EgressMode: EgressDirect,
		IngressProtocol: IngressVLESSReality, VLESSFlow: "xtls-rprx-vision", RealityServerName: "www.example.com",
		RealityPublicKey: "AbCdEf0123456789AbCdEf0123456789AbCdEf01234", RealityShortID: "0123456789abcdef",
		Protocol: ProtocolTCP, ListenPort: 24443, Targets: []Target{{Host: "target.example.com", Port: 443}},
		SelectionPolicy: SelectionRoundRobin, Revision: 3,
	}
	request := requestFromRule(rule)
	if request.IngressProtocol != IngressVLESSReality || request.VLESSFlow != rule.VLESSFlow || request.RealityServerName != rule.RealityServerName || request.RealityPublicKey != rule.RealityPublicKey || request.RealityShortID != rule.RealityShortID {
		t.Fatalf("export projection dropped Reality fields: %+v", request)
	}
}

func TestExportProjectionPreservesVLESSSOCKS5Parameters(t *testing.T) {
	rule := Rule{
		Name: "Reality SOCKS5 规则", CustomerID: "customer-1", EntryGroupID: "entry-1", EgressMode: EgressDirect,
		VLESSOutboundMode: VLESSOutboundSOCKS5, VLESSSOCKS5Host: "landing.example.com", VLESSSOCKS5Port: 1080,
		VLESSSOCKS5Username: "relay-user", IngressProtocol: IngressVLESSReality, VLESSFlow: "xtls-rprx-vision",
		RealityServerName: "www.example.com", RealityPublicKey: "AbCdEf0123456789AbCdEf0123456789AbCdEf01234",
		RealityShortID: "0123456789abcdef", Protocol: ProtocolTCP, ListenPort: 24443,
		SelectionPolicy: SelectionRoundRobin, Revision: 3,
	}
	request := requestFromRule(rule)
	if request.VLESSOutboundMode != VLESSOutboundSOCKS5 || request.VLESSSOCKS5Host != rule.VLESSSOCKS5Host || request.VLESSSOCKS5Port != rule.VLESSSOCKS5Port || request.VLESSSOCKS5Username != rule.VLESSSOCKS5Username {
		t.Fatalf("export projection dropped VLESS SOCKS5 fields: %+v", request)
	}
	if request.VLESSSOCKS5Password != "" {
		t.Fatal("export projection must not include VLESS SOCKS5 password")
	}

	// An exported document deliberately has no password. Preview of an
	// unauthenticated SOCKS5 rule remains valid; authenticated imports must
	// supply the password again through the write API.
	previewRequest := request
	previewRequest.VLESSSOCKS5Username = ""
	document := ExportDocument{SchemaVersion: ExportSchemaV1, ExportedAt: time.Now().UTC(), Rules: []ExportRule{{Operation: ImportUpsert, ID: rule.ID, Rule: previewRequest}}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := NewService(&captureRepository{}, nil).PreviewImport(context.Background(), TransferRequest{Format: ImportFormatJSONV1, Content: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	row := preview.Rows[0]
	if preview.Invalid != 0 || row.Request.VLESSOutboundMode != VLESSOutboundSOCKS5 || row.Request.VLESSSOCKS5Host != rule.VLESSSOCKS5Host || row.Request.VLESSSOCKS5Port != rule.VLESSSOCKS5Port || row.Request.VLESSSOCKS5Username != "" {
		t.Fatalf("VLESS SOCKS5 export/import preview lost fields: %+v", row)
	}
}

func TestImportMaximumIsEnforcedBeforeRepository(t *testing.T) {
	lines := make([]string, MaxImportRules+1)
	for index := range lines {
		lines[index] = "rule##example.test#443"
	}
	preview, err := NewService(&captureRepository{}, nil).PreviewImport(context.Background(), TransferRequest{
		Format: ImportFormatNYText, Content: strings.Join(lines, "\n"),
		Mapping: ImportMapping{CustomerID: "customer-1", EntryGroupID: "entry-1", EgressMode: EgressDirect, Protocol: ProtocolTCP, SelectionPolicy: SelectionRoundRobin},
	})
	if err != nil || len(preview.GlobalIssues) != 1 || preview.GlobalIssues[0].Code != "too_many_rules" || preview.CanCommit() {
		t.Fatalf("maximum was not enforced: %+v %v", preview, err)
	}
}

func TestInvalidImportTargetIsNotReflectedInPreview(t *testing.T) {
	preview, err := NewService(&captureRepository{}, nil).PreviewImport(context.Background(), TransferRequest{
		Format: ImportFormatNYText, Content: "unsafe##user:secret@example.test#443",
		Mapping: ImportMapping{CustomerID: "customer-1", EntryGroupID: "entry-1", EgressMode: EgressDirect, Protocol: ProtocolTCP, SelectionPolicy: SelectionRoundRobin},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(preview)
	if preview.Invalid != 1 || strings.Contains(string(raw), "user:secret") {
		t.Fatalf("unsafe target was reflected: %s", raw)
	}
}
