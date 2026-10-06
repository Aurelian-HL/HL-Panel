package memoryrepo

import (
	"context"
	"fmt"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func (s *Store) PreviewForwardingImport(_ context.Context, candidates []forwarding.ImportCandidate) ([]forwarding.ImportEvaluation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	evaluations, _ := s.evaluateForwardingImportLocked(candidates)
	return evaluations, nil
}

func (s *Store) ImportForwardingRules(_ context.Context, input forwarding.ImportInput, event audit.Event) (forwarding.ImportResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.CreatedBy + "\x00forwarding.import\x00" + input.IdempotencyKey
	var replay forwarding.ImportResult
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		if ok {
			for index := range replay.Items {
				replay.Items[index] = s.forwardingViewLocked(replay.Items[index])
			}
		}
		return replay, ok, err
	}
	autoNetworks := make([]string, 0)
	seenAutoNetworks := make(map[string]bool)
	for _, candidate := range input.Candidates {
		if candidate.Rule.EntryGroupID == "" || seenAutoNetworks[candidate.Rule.EntryGroupID] {
			continue
		}
		_, created, err := s.ensureEntryGroupNetworkLocked(candidate.Rule.EntryGroupID)
		if err != nil {
			return forwarding.ImportResult{}, false, err
		}
		if created {
			autoNetworks = append(autoNetworks, candidate.Rule.EntryGroupID)
			seenAutoNetworks[candidate.Rule.EntryGroupID] = true
		}
	}
	rollbackAutoNetworks := func() {
		for _, groupID := range autoNetworks {
			delete(s.groupNetworks, groupID)
		}
	}
	evaluations, working := s.evaluateForwardingImportLocked(input.Candidates)
	affectedGroups := make([]string, 0, len(input.Candidates)*2)
	for _, candidate := range input.Candidates {
		if candidate.Rule.EntryGroupID != "" {
			affectedGroups = append(affectedGroups, candidate.Rule.EntryGroupID)
		}
		if previous, exists := s.forwardRules[candidate.Rule.ID]; exists && previous.EntryGroupID != "" {
			affectedGroups = append(affectedGroups, previous.EntryGroupID)
		}
	}
	result := forwarding.ImportResult{Items: make([]forwarding.Rule, 0, len(evaluations))}
	for _, evaluation := range evaluations {
		if evaluation.Issue != nil {
			kind := faults.ErrValidation
			if strings.Contains(evaluation.Issue.Code, "conflict") || evaluation.Issue.Code == "missing_resource" {
				kind = faults.ErrConflict
			}
			rollbackAutoNetworks()
			return forwarding.ImportResult{}, false, fmt.Errorf("%w: import row %d: %s", kind, evaluation.Line, evaluation.Issue.Message)
		}
		if evaluation.Action == forwarding.ImportActionCreate {
			result.Created++
		} else {
			result.Updated++
		}
		result.Items = append(result.Items, s.forwardingViewLocked(evaluation.Rule))
	}
	if len(result.Items) == 0 {
		rollbackAutoNetworks()
		return forwarding.ImportResult{}, false, fmt.Errorf("%w: import must contain at least one rule", faults.ErrValidation)
	}
	if len(autoNetworks) > 0 {
		if event.Metadata == nil {
			event.Metadata = make(map[string]any)
		}
		event.Metadata["entry_network_defaulted"] = true
		event.Metadata["entry_network_defaulted_groups"] = len(autoNetworks)
	}
	if event.Metadata == nil {
		event.Metadata = make(map[string]any)
	}
	event.Metadata["created"] = result.Created
	event.Metadata["updated"] = result.Updated
	if err := s.recordBusinessLocked(key, input.RequestSHA256, event.ResourceID, result); err != nil {
		rollbackAutoNetworks()
		return forwarding.ImportResult{}, false, err
	}
	originalRules := s.forwardRules
	s.forwardRules = working
	if err := s.recompileForwardingGroupsLocked(affectedGroups, event.CreatedAt); err != nil {
		s.forwardRules = originalRules
		delete(s.businessIdempotency, key)
		rollbackAutoNetworks()
		return forwarding.ImportResult{}, false, err
	}
	s.appendAuditLocked(event)
	return result, false, nil
}

func (s *Store) evaluateForwardingImportLocked(candidates []forwarding.ImportCandidate) ([]forwarding.ImportEvaluation, map[string]forwarding.Rule) {
	working := make(map[string]forwarding.Rule, len(s.forwardRules)+len(candidates))
	for id, rule := range s.forwardRules {
		working[id] = cloneForwardingRule(rule)
	}
	evaluations := make([]forwarding.ImportEvaluation, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		evaluation := forwarding.ImportEvaluation{Line: candidate.Line}
		item := cloneForwardingRule(candidate.Rule)
		if seen[item.ID] {
			evaluation.Issue = importIssue("revision_conflict", "同一导入文件不能重复修改同一规则")
			evaluations = append(evaluations, evaluation)
			continue
		}
		seen[item.ID] = true
		previous, exists := working[item.ID]
		if exists && (item.OwnerKind == forwarding.OwnerAdministrator && !previous.OwnedByAdministrator(item.OwnerID) || previous.OwnerKind == forwarding.OwnerAdministrator && (item.OwnerKind != forwarding.OwnerAdministrator || item.OwnerID != previous.OwnerID)) {
			evaluation.Issue = importIssue("missing_resource", "规则不属于当前管理员")
			evaluations = append(evaluations, evaluation)
			continue
		}
		switch candidate.Operation {
		case forwarding.ImportCreate:
			if exists {
				evaluation.Issue = importIssue("revision_conflict", "create 操作的规则 id 已存在")
			}
			evaluation.Action = forwarding.ImportActionCreate
		case forwarding.ImportUpdate:
			if !exists {
				evaluation.Issue = importIssue("missing_resource", "update 操作引用的规则不存在")
			}
			evaluation.Action = forwarding.ImportActionUpdate
		case forwarding.ImportUpsert:
			if exists {
				evaluation.Action = forwarding.ImportActionUpdate
			} else {
				evaluation.Action = forwarding.ImportActionCreate
			}
		default:
			evaluation.Issue = importIssue("validation", "不支持的导入操作")
		}
		if evaluation.Issue != nil {
			evaluations = append(evaluations, evaluation)
			continue
		}
		if evaluation.Action == forwarding.ImportActionUpdate {
			if previous.CustomerID != item.CustomerID || previous.OwnerKind != item.OwnerKind || previous.OwnerID != item.OwnerID {
				evaluation.Issue = importIssue("missing_resource", "update 操作引用的规则不存在")
				evaluations = append(evaluations, evaluation)
				continue
			}
			if candidate.ExpectedRevision < 1 || previous.Revision != candidate.ExpectedRevision {
				evaluation.Issue = importIssue("revision_conflict", "规则已变更，请重新导出或刷新后再导入")
				evaluations = append(evaluations, evaluation)
				continue
			}
			item.Revision = previous.Revision + 1
			item.CreatedAt = previous.CreatedAt
			if item.ListenPort == 0 && item.EntryGroupID == previous.EntryGroupID && item.Protocol == previous.Protocol {
				item.ListenPort = previous.ListenPort
			}
		} else {
			item.Revision = 1
		}
		if err := s.prepareForwardingRuleAgainstLocked(&item, working); err != nil {
			issue := forwardingImportIssue(err)
			evaluation.Issue = &issue
			evaluations = append(evaluations, evaluation)
			continue
		}
		working[item.ID] = cloneForwardingRule(item)
		evaluation.Rule = s.forwardingViewLocked(item)
		evaluations = append(evaluations, evaluation)
	}
	return evaluations, working
}

func importIssue(code, message string) *forwarding.ImportIssue {
	return &forwarding.ImportIssue{Code: code, Message: message}
}

func forwardingImportIssue(err error) forwarding.ImportIssue {
	code := "validation"
	message := err.Error()
	switch {
	case strings.Contains(message, "already reserved") || strings.Contains(message, "no free port"):
		code = "port_conflict"
	case strings.Contains(message, "not authorized") || strings.Contains(message, "authorization"):
		code = "permission_conflict"
	case strings.Contains(message, "does not exist"):
		code = "missing_resource"
	case strings.Contains(message, "quota"):
		code = "quota_conflict"
	}
	message = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(message, faults.ErrValidation.Error()+":"), faults.ErrConflict.Error()+":"))
	return forwarding.ImportIssue{Code: code, Message: message}
}
