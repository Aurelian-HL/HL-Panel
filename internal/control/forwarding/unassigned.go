package forwarding

// An unassigned source listener is not an administrator-owned customer line.
// Only maintenance import can create its native runtime and source provenance.
func (r Rule) IsUnassignedImport() bool {
	return r.OwnerKind == OwnerUnassigned && r.OwnerID == "" && r.VLESSOutboundMode == VLESSOutboundNative && r.CustomerID == "klunassigned_"+r.EntryGroupID
}
