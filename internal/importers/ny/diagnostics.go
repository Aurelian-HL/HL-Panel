package ny

import (
	"errors"
	"sort"
)

func validateAdapterDiagnostics(preview AdapterPreview) error {
	groups := fingerprintSet(preview.Groups.Fingerprints)
	rules := fingerprintSet(preview.Rules.Fingerprints)
	for _, conflict := range preview.ReferenceConflicts {
		if conflict.Code != ReasonMissingGroup || conflict.FromType != "rule" ||
			!rules[conflict.FromFingerprint] || conflict.ToType != "group" ||
			conflict.Field != "entry_group_id" && conflict.Field != "exit_group_id" && conflict.Field != "group_id" ||
			conflict.ToFingerprint != "" && !validSHA256(conflict.ToFingerprint) {
			return errors.New("adapter reference conflict contains untrusted diagnostics")
		}
		if groups[conflict.ToFingerprint] {
			return errors.New("adapter reported a missing group that is present in the preview")
		}
	}
	for _, field := range preview.UnsupportedFields {
		if field.Code != "FIELD_UNSUPPORTED" || field.Path != "" && field.PathSHA256 != "" ||
			field.Path == "" && !validSHA256(field.PathSHA256) {
			return errors.New("adapter unsupported field contains untrusted diagnostics")
		}
	}
	for _, item := range preview.Issues {
		if adapterIssueMessage(item.Code) == "" || item.Path != "" ||
			item.Severity != SeverityWarning && item.Severity != SeverityError {
			return errors.New("adapter issue contains untrusted diagnostics")
		}
	}
	return nil
}

func adapterIssueMessage(code string) string {
	switch code {
	case ReasonMissingGroup:
		return "source rule refers to a group not present in the snapshot"
	case ReasonUnknownField:
		return "source contains a field not mapped by the adapter"
	case ReasonUnsupported:
		return "source semantics cannot be mapped without operator review"
	default:
		return ""
	}
}

func cloneMappings(mappings []MappingProposal) []MappingProposal {
	result := make([]MappingProposal, 0, len(mappings))
	for _, mapping := range mappings {
		mapping.ReasonCodes = append([]string{}, mapping.ReasonCodes...)
		sort.Strings(mapping.ReasonCodes)
		result = append(result, mapping)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SourceFingerprint < result[j].SourceFingerprint })
	return result
}

func canonicalizeAdapterReport(report *Report) {
	sort.Slice(report.ReferenceConflicts, func(i, j int) bool {
		left, right := report.ReferenceConflicts[i], report.ReferenceConflicts[j]
		if left.FromFingerprint != right.FromFingerprint {
			return left.FromFingerprint < right.FromFingerprint
		}
		return left.Field < right.Field
	})
	sort.Slice(report.UnsupportedFields, func(i, j int) bool {
		left, right := report.UnsupportedFields[i], report.UnsupportedFields[j]
		if left.PathSHA256 != right.PathSHA256 {
			return left.PathSHA256 < right.PathSHA256
		}
		return left.Code < right.Code
	})
	sort.Slice(report.Issues, func(i, j int) bool {
		left, right := report.Issues[i], report.Issues[j]
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.Severity < right.Severity
	})
}
