package ny

import (
	"errors"
)

// MappingProposal is an opaque, read-only projection of an adapter's intended
// target object. It is not a database import plan or a reserved target ID.
type MappingProposal struct {
	SourceFingerprint string   `json:"source_fingerprint"`
	TargetKind        string   `json:"target_kind"`
	GroupKind         string   `json:"group_kind,omitempty"`
	EntryGroup        string   `json:"entry_group_fingerprint,omitempty"`
	ExitGroup         string   `json:"exit_group_fingerprint,omitempty"`
	EgressMode        string   `json:"egress_mode,omitempty"`
	Disposition       string   `json:"disposition"`
	ReasonCodes       []string `json:"reason_codes"`
}

const (
	TargetDeviceGroup    = "device_group"
	TargetForwardingRule = "forwarding_rule"
	GroupKindEntry       = "ENTRY"
	GroupKindExit        = "EXIT"
	EgressDirect         = "DIRECT"
	EgressExitGroup      = "EXIT_GROUP"
	DispositionProposed  = "proposed"
	DispositionBlocked   = "blocked"
	ReasonMissingGroup   = "MISSING_GROUP_REFERENCE"
	ReasonUnknownField   = "UNSUPPORTED_FIELD"
	ReasonUnsupported    = "UNSUPPORTED_SEMANTICS"
)

func validateMappings(preview AdapterPreview) error {
	groups := fingerprintSet(preview.Groups.Fingerprints)
	rules := fingerprintSet(preview.Rules.Fingerprints)
	if len(preview.Mappings) != len(groups)+len(rules) {
		return errors.New("mapping proposals must cover each previewed group and rule exactly once")
	}
	seen := make(map[string]bool, len(preview.Mappings))
	groupKinds := make(map[string]string, len(groups))
	groupDispositions := make(map[string]string, len(groups))
	for _, mapping := range preview.Mappings {
		if mapping.TargetKind == TargetDeviceGroup {
			groupKinds[mapping.SourceFingerprint] = mapping.GroupKind
			groupDispositions[mapping.SourceFingerprint] = mapping.Disposition
		}
	}
	for _, mapping := range preview.Mappings {
		if !validSHA256(mapping.SourceFingerprint) || seen[mapping.SourceFingerprint] {
			return errors.New("mapping source fingerprint is invalid or repeated")
		}
		seen[mapping.SourceFingerprint] = true
		switch mapping.TargetKind {
		case TargetDeviceGroup:
			if !groups[mapping.SourceFingerprint] || mapping.EntryGroup != "" || mapping.ExitGroup != "" || mapping.EgressMode != "" {
				return errors.New("device-group mapping does not match a source group")
			}
			if mapping.Disposition == DispositionProposed && mapping.GroupKind != GroupKindEntry && mapping.GroupKind != GroupKindExit {
				return errors.New("proposed device group requires a supported group kind")
			}
			if mapping.GroupKind != "" && mapping.GroupKind != GroupKindEntry && mapping.GroupKind != GroupKindExit {
				return errors.New("device-group mapping has an unsupported group kind")
			}
		case TargetForwardingRule:
			if !rules[mapping.SourceFingerprint] || mapping.GroupKind != "" {
				return errors.New("forwarding-rule mapping does not match a source rule")
			}
			if mapping.Disposition == DispositionProposed {
				if !groups[mapping.EntryGroup] || groupKinds[mapping.EntryGroup] != GroupKindEntry ||
					groupDispositions[mapping.EntryGroup] != DispositionProposed {
					return errors.New("proposed forwarding rule requires a proposed entry group")
				}
				switch mapping.EgressMode {
				case EgressDirect:
					if mapping.ExitGroup != "" {
						return errors.New("direct rule cannot reference an exit group")
					}
				case EgressExitGroup:
					if !groups[mapping.ExitGroup] || groupKinds[mapping.ExitGroup] != GroupKindExit ||
						groupDispositions[mapping.ExitGroup] != DispositionProposed {
						return errors.New("exit-group rule requires a proposed exit group")
					}
				default:
					return errors.New("proposed forwarding rule requires an explicit egress mode")
				}
			} else if mapping.EntryGroup != "" || mapping.ExitGroup != "" || mapping.EgressMode != "" {
				return errors.New("blocked forwarding rule cannot claim a route mapping")
			}
		default:
			return errors.New("mapping target kind is unsupported")
		}
		if mapping.Disposition != DispositionProposed && mapping.Disposition != DispositionBlocked {
			return errors.New("mapping disposition is unsupported")
		}
		if mapping.Disposition == DispositionBlocked && len(mapping.ReasonCodes) == 0 {
			return errors.New("blocked mapping requires a reason code")
		}
		if mapping.Disposition == DispositionProposed && len(mapping.ReasonCodes) != 0 {
			return errors.New("proposed mapping cannot carry blocking reasons")
		}
		for _, reason := range mapping.ReasonCodes {
			switch reason {
			case ReasonMissingGroup, ReasonUnknownField, ReasonUnsupported:
			default:
				return errors.New("mapping has an unsupported reason code")
			}
		}
	}
	for _, conflict := range preview.ReferenceConflicts {
		if conflict.FromType == "rule" && !blockedMappingFor(preview.Mappings, conflict.FromFingerprint, ReasonMissingGroup) {
			return errors.New("rule with a missing-group reference must be blocked for that reason")
		}
	}
	return nil
}

func fingerprintSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func blockedMappingFor(mappings []MappingProposal, fingerprint, reason string) bool {
	for _, mapping := range mappings {
		if mapping.SourceFingerprint == fingerprint && mapping.Disposition == DispositionBlocked {
			for _, candidate := range mapping.ReasonCodes {
				if candidate == reason {
					return true
				}
			}
		}
	}
	return false
}
