package forwarding

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/hongle/hl-panel/internal/control/faults"
)

type parsedImportRow struct {
	line      int
	operation ImportOperation
	id        string
	request   Request
	issues    []ImportIssue
}

type exportDocumentInput struct {
	SchemaVersion string          `json:"schema_version"`
	ExportedAt    json.RawMessage `json:"exported_at,omitempty"`
	Rules         []ExportRule    `json:"rules"`
}

func parseTransferRequest(input TransferRequest) (ImportFormat, []parsedImportRow, []ImportIssue) {
	format := input.Format
	if format == "" || format == ImportFormatAuto {
		if strings.HasPrefix(strings.TrimSpace(input.Content), "{") {
			format = ImportFormatJSONV1
		} else {
			format = ImportFormatNYText
		}
	}
	if strings.TrimSpace(input.Content) == "" {
		return format, nil, []ImportIssue{{Code: "empty_content", Message: "导入内容不能为空"}}
	}
	var rows []parsedImportRow
	var global []ImportIssue
	switch format {
	case ImportFormatNYText:
		rows, global = parseNYText(input.Content, input.Mapping)
	case ImportFormatJSONV1:
		rows, global = parseJSONV1(input.Content, input.Mapping)
	default:
		global = []ImportIssue{{Code: "unsupported_format", Message: "导入格式必须是 auto、ny_text 或 json_v1"}}
	}
	if len(rows) > MaxImportRules {
		global = append(global, ImportIssue{Code: "too_many_rules", Message: fmt.Sprintf("单次最多导入 %d 条规则", MaxImportRules)})
	}
	if len(rows) == 0 && len(global) == 0 {
		global = append(global, ImportIssue{Code: "empty_content", Message: "没有可导入的规则"})
	}
	return format, rows, global
}

func parseNYText(content string, mapping ImportMapping) ([]parsedImportRow, []ImportIssue) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	// A line cannot carry arbitrary payloads; this allows long DNS labels and
	// descriptions while bounding scanner memory below the HTTP body limit.
	scanner.Buffer(make([]byte, 4096), 64*1024)
	rows := make([]parsedImportRow, 0)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		if text == "" {
			continue
		}
		row := parsedImportRow{line: line, operation: ImportCreate}
		parts := strings.Split(text, "#")
		if len(parts) != 4 {
			row.issues = append(row.issues, ImportIssue{Code: "invalid_columns", Message: "必须使用 名称#监听端口#目标地址#目标端口 四列格式"})
			rows = append(rows, row)
			continue
		}
		listenPort := 0
		if strings.TrimSpace(parts[1]) != "" {
			value, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				row.issues = append(row.issues, ImportIssue{Code: "invalid_listen_port", Message: "监听端口必须留空或填写 1 到 65535"})
			} else {
				listenPort = value
			}
		}
		targetPort, err := strconv.Atoi(strings.TrimSpace(parts[3]))
		if err != nil {
			row.issues = append(row.issues, ImportIssue{Code: "invalid_target_port", Message: "目标端口必须填写 1 到 65535"})
		}
		row.request = Request{
			Name: strings.TrimSpace(parts[0]), CustomerID: mapping.CustomerID, RuleGroupID: mapping.RuleGroupID,
			EntryGroupID: mapping.EntryGroupID, ExitGroupID: mapping.ExitGroupID, EgressMode: mapping.EgressMode,
			IngressProtocol: mapping.IngressProtocol,
			Protocol:        mapping.Protocol, ListenPort: listenPort,
			Targets: []Target{{Host: strings.TrimSpace(parts[2]), Port: targetPort}}, SelectionPolicy: mapping.SelectionPolicy,
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return rows, []ImportIssue{{Code: "line_too_long", Message: "导入文本包含超过 64 KiB 的单行"}}
	}
	return rows, nil
}

func parseJSONV1(content string, mapping ImportMapping) ([]parsedImportRow, []ImportIssue) {
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	var document exportDocumentInput
	if err := decoder.Decode(&document); err != nil {
		return nil, []ImportIssue{{Code: "invalid_json", Message: "JSON 文件无法解析或包含未知字段"}}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, []ImportIssue{{Code: "invalid_json", Message: "JSON 文件只能包含一个根对象"}}
	}
	if document.SchemaVersion != ExportSchemaV1 {
		return nil, []ImportIssue{{Code: "unsupported_schema", Message: "仅支持 " + ExportSchemaV1 + " 格式"}}
	}
	rows := make([]parsedImportRow, 0, len(document.Rules))
	for index, item := range document.Rules {
		row := parsedImportRow{line: index + 1, id: strings.TrimSpace(item.ID), operation: item.Operation, request: item.Rule}
		if row.operation == "" {
			row.operation = ImportUpsert
		}
		if row.operation != ImportCreate && row.operation != ImportUpdate && row.operation != ImportUpsert {
			row.issues = append(row.issues, ImportIssue{Code: "invalid_operation", Message: "operation 必须是 create、update 或 upsert"})
		}
		applyMapping(&row.request, mapping)
		rows = append(rows, row)
	}
	return rows, nil
}

func applyMapping(request *Request, mapping ImportMapping) {
	if value := strings.TrimSpace(mapping.CustomerID); value != "" {
		request.CustomerID = value
	}
	if value := strings.TrimSpace(mapping.RuleGroupID); value != "" {
		request.RuleGroupID = value
	}
	if value := strings.TrimSpace(mapping.EntryGroupID); value != "" {
		request.EntryGroupID = value
	}
	if mapping.EgressMode != "" {
		request.EgressMode = mapping.EgressMode
	}
	if mapping.EgressMode == EgressDirect {
		request.ExitGroupID = ""
	}
	if mapping.IngressProtocol != "" {
		request.IngressProtocol = mapping.IngressProtocol
	}
	if value := strings.TrimSpace(mapping.ExitGroupID); value != "" && request.EgressMode == EgressExitGroup {
		request.ExitGroupID = value
	}
	if mapping.Protocol != "" {
		request.Protocol = mapping.Protocol
	}
	if mapping.SelectionPolicy != "" {
		request.SelectionPolicy = mapping.SelectionPolicy
	}
}

func issueForError(err error) ImportIssue {
	code := "validation"
	switch {
	case err == nil:
		return ImportIssue{}
	case strings.Contains(err.Error(), "changed; reload"):
		code = "revision_conflict"
	case strings.Contains(err.Error(), "already reserved") || strings.Contains(err.Error(), "no free port"):
		code = "port_conflict"
	case strings.Contains(err.Error(), "not authorized") || strings.Contains(err.Error(), "authorization"):
		code = "permission_conflict"
	case strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "not found"):
		code = "missing_resource"
	case strings.Contains(err.Error(), "quota"):
		code = "quota_conflict"
	}
	message := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(err.Error(), faults.ErrValidation.Error()+":"), faults.ErrConflict.Error()+":"))
	return ImportIssue{Code: code, Message: message}
}
