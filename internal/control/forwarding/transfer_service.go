package forwarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
)

func (s *Service) PreviewImport(ctx context.Context, input TransferRequest) (ImportPreview, error) {
	return s.previewImport(ctx, "", input)
}

func (s *Service) PreviewImportForAdministrator(ctx context.Context, adminID string, input TransferRequest) (ImportPreview, error) {
	return s.previewImport(ctx, adminID, input)
}

func (s *Service) previewImport(ctx context.Context, adminID string, input TransferRequest) (ImportPreview, error) {
	format, parsed, global := parseTransferRequest(input)
	preview, candidates := s.normalizeImportRowsForOwner(format, parsed, global, true, adminID)
	if len(candidates) == 0 || len(preview.GlobalIssues) > 0 {
		return preview, nil
	}
	evaluations, err := s.repository.PreviewForwardingImport(ctx, candidates)
	if err != nil {
		return ImportPreview{}, err
	}
	applyImportEvaluations(&preview, evaluations)
	return preview, nil
}

func (s *Service) Import(ctx context.Context, adminID string, input TransferRequest, idempotencyKey string) (ImportResult, bool, error) {
	return s.importRules(ctx, adminID, "", input, idempotencyKey)
}

func (s *Service) ImportForAdministrator(ctx context.Context, adminID string, input TransferRequest, idempotencyKey string) (ImportResult, bool, error) {
	return s.importRules(ctx, adminID, adminID, input, idempotencyKey)
}

func (s *Service) importRules(ctx context.Context, adminID, ownerAdminID string, input TransferRequest, idempotencyKey string) (ImportResult, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validReference(idempotencyKey) {
		return ImportResult{}, false, fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 non-space characters", faults.ErrValidation)
	}
	format, parsed, global := parseTransferRequest(input)
	preview, candidates := s.normalizeImportRowsForOwner(format, parsed, global, false, ownerAdminID)
	if preview.Invalid > 0 || len(preview.GlobalIssues) > 0 || preview.Total == 0 {
		return ImportResult{}, false, fmt.Errorf("%w: import contains %d invalid rows", faults.ErrValidation, preview.Invalid)
	}
	canonical, err := json.Marshal(TransferRequest{Format: format, Content: input.Content, Mapping: input.Mapping})
	if err != nil {
		return ImportResult{}, false, err
	}
	digest := sha256.Sum256(canonical)
	now := s.now().UTC()
	batchID, err := idgen.New("imp")
	if err != nil {
		return ImportResult{}, false, err
	}
	event, err := audit.NewEvent(now, "administrator", adminID, "forwarding_rule.import", "forwarding_rule_import", batchID, "succeeded", map[string]any{
		"format": format, "rule_count": len(candidates),
	})
	if err != nil {
		return ImportResult{}, false, err
	}
	createdBy := adminID
	if ownerAdminID != "" {
		createdBy = "admin-rules:" + adminID
	}
	return s.repository.ImportForwardingRules(ctx, ImportInput{
		Candidates: candidates, IdempotencyKey: idempotencyKey,
		RequestSHA256: hex.EncodeToString(digest[:]), CreatedBy: createdBy,
	}, event)
}

func (s *Service) Export(ctx context.Context) (ExportDocument, error) {
	items, err := s.repository.ListForwardingRules(ctx)
	if err != nil {
		return ExportDocument{}, err
	}
	document := ExportDocument{SchemaVersion: ExportSchemaV1, ExportedAt: s.now().UTC(), Rules: make([]ExportRule, len(items))}
	for index, item := range items {
		document.Rules[index] = ExportRule{Operation: ImportUpsert, ID: item.ID, Rule: requestFromRule(item)}
	}
	return document, nil
}

func (s *Service) ExportForAdministrator(ctx context.Context, adminID string) (ExportDocument, error) {
	items, err := s.ListForAdministrator(ctx, adminID)
	if err != nil {
		return ExportDocument{}, err
	}
	document := ExportDocument{SchemaVersion: ExportSchemaV1, ExportedAt: s.now().UTC(), Rules: make([]ExportRule, len(items))}
	for index, item := range items {
		document.Rules[index] = ExportRule{Operation: ImportUpsert, ID: item.ID, Rule: requestFromRule(item)}
	}
	return document, nil
}

func (s *Service) normalizeImportRows(format ImportFormat, parsed []parsedImportRow, global []ImportIssue, previewOnly bool) (ImportPreview, []ImportCandidate) {
	return s.normalizeImportRowsForOwner(format, parsed, global, previewOnly, "")
}

func (s *Service) normalizeImportRowsForOwner(format ImportFormat, parsed []parsedImportRow, global []ImportIssue, previewOnly bool, adminID string) (ImportPreview, []ImportCandidate) {
	preview := ImportPreview{Format: format, Total: len(parsed), Rows: make([]ImportRow, len(parsed)), GlobalIssues: append([]ImportIssue{}, global...)}
	candidates := make([]ImportCandidate, 0, len(parsed))
	for index, parsedRow := range parsed {
		if adminID != "" {
			parsedRow.request.CustomerID = AdministratorSubjectID(adminID)
			parsedRow.request.OwnerKind, parsedRow.request.OwnerID = OwnerAdministrator, adminID
		}
		row := ImportRow{Line: parsedRow.line, Operation: parsedRow.operation, SourceID: parsedRow.id, Request: parsedRow.request, Issues: append([]ImportIssue{}, parsedRow.issues...)}
		if len(row.Issues) == 0 {
			normalized, err := NormalizeRequest(parsedRow.request)
			if err != nil {
				row.Issues = append(row.Issues, issueForError(err))
				// Invalid target text may contain credentials. The row and error are
				// useful without reflecting the untrusted target back to the client.
				row.Request.Targets = []Target{}
			} else if parsedRow.id != "" && !validReference(parsedRow.id) {
				row.Issues = append(row.Issues, ImportIssue{Code: "invalid_id", Message: "规则 id 必须是 1 到 128 位且不能包含空白字符"})
			} else if parsedRow.operation == ImportCreate && normalized.Revision != 0 {
				row.Issues = append(row.Issues, ImportIssue{Code: "invalid_revision", Message: "create 操作的 revision 必须为 0"})
			} else if parsedRow.operation == ImportUpdate && parsedRow.id == "" {
				row.Issues = append(row.Issues, ImportIssue{Code: "missing_id", Message: "update 操作必须提供规则 id"})
			} else if (parsedRow.operation == ImportUpdate || parsedRow.operation == ImportUpsert) && parsedRow.id != "" && normalized.Revision < 1 {
				row.Issues = append(row.Issues, ImportIssue{Code: "missing_revision", Message: "update/upsert 操作必须提供有效 revision"})
			} else {
				row.Request = normalized
				id := parsedRow.id
				if id == "" {
					if previewOnly {
						id = fmt.Sprintf("preview-import-%d", index+1)
					} else {
						id, err = idgen.New("fwd")
						if err != nil {
							row.Issues = append(row.Issues, ImportIssue{Code: "id_generation", Message: "无法生成规则编号"})
						}
					}
				}
				if len(row.Issues) == 0 {
					now := s.now().UTC()
					rule := normalized.rule(id, now)
					rule.Revision = 1
					candidates = append(candidates, ImportCandidate{Line: parsedRow.line, Operation: parsedRow.operation, Rule: rule, ExpectedRevision: normalized.Revision})
				}
			}
		}
		preview.Rows[index] = row
	}
	preview.recount()
	return preview, candidates
}

func applyImportEvaluations(preview *ImportPreview, evaluations []ImportEvaluation) {
	byLine := make(map[int]ImportEvaluation, len(evaluations))
	for _, evaluation := range evaluations {
		byLine[evaluation.Line] = evaluation
	}
	for index := range preview.Rows {
		row := &preview.Rows[index]
		if len(row.Issues) > 0 {
			continue
		}
		evaluation, found := byLine[row.Line]
		if !found {
			row.Issues = append(row.Issues, ImportIssue{Code: "validation", Message: "规则未完成验证"})
			continue
		}
		row.Action = evaluation.Action
		row.ListenPort = evaluation.Rule.ListenPort
		if evaluation.Issue != nil {
			row.Issues = append(row.Issues, *evaluation.Issue)
		}
	}
	preview.recount()
}

func (p *ImportPreview) recount() {
	p.Valid, p.Invalid = 0, 0
	for _, row := range p.Rows {
		if len(row.Issues) == 0 {
			p.Valid++
		} else {
			p.Invalid++
		}
	}
}

func requestFromRule(rule Rule) Request {
	return Request{Name: rule.Name, CustomerID: rule.CustomerID, OwnerKind: rule.OwnerKind, OwnerID: rule.OwnerID, RuleGroupID: rule.RuleGroupID, EntryGroupID: rule.EntryGroupID,
		ExitGroupID: rule.ExitGroupID, EgressMode: rule.EgressMode, VLESSOutboundMode: rule.VLESSOutboundMode,
		VLESSSOCKS5Host: rule.VLESSSOCKS5Host, VLESSSOCKS5Port: rule.VLESSSOCKS5Port, VLESSSOCKS5Username: rule.VLESSSOCKS5Username,
		IngressProtocol: rule.EffectiveIngressProtocol(),
		VLESSFlow:       rule.VLESSFlow, RealityServerName: rule.RealityServerName, RealityPublicKey: rule.RealityPublicKey,
		RealityShortID: rule.RealityShortID, RealityDestination: rule.RealityDestination, Protocol: rule.Protocol, ListenPort: rule.ListenPort,
		Targets: append([]Target(nil), rule.Targets...), SelectionPolicy: rule.SelectionPolicy,
		AcceptProxyProtocol: rule.AcceptProxyProtocol, SendProxyProtocol: rule.SendProxyProtocol,
		SpeedLimitMbps: rule.SpeedLimitMbps, IPLimit: rule.IPLimit, ConnectionLimit: rule.ConnectionLimit,
		Paused: rule.Paused, Description: rule.Description, Revision: rule.Revision}
}
