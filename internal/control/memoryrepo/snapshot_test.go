package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func snapshotFixture(t *testing.T) *Store {
	t.Helper()
	hash, err := auth.HashPassword("unit-test-bootstrap-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	s := New(auth.Administrator{ID: "adm-1", Username: "admin", PasswordHash: hash, CreatedAt: now})
	s.sessionsByHash["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"] = auth.Session{ID: "session-1", AdminID: "adm-1", TokenHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	s.tokensByHash["token-hash"] = enrollment.Token{ID: "token-1", TokenHash: "token-hash", UsedAt: &now, ExpiresAt: now.Add(time.Hour)}
	s.nodes["node-1"] = nodes.Node{ID: "node-1", CredentialHash: "credential-hash", DesiredGeneration: 1, LastHeartbeatAt: &now, Resources: map[string]any{"load": 0.25}}
	s.nodesByCredential["credential-hash"] = "node-1"
	s.deviceGroups["group-1"] = groups.DeviceGroup{ID: "group-1", Name: "test", CurrentRevision: 1, MetadataRevision: 1}
	s.membersByGroup["group-1"] = map[string]groups.Member{"node-1": {GroupID: "group-1", NodeID: "node-1", Weight: 2}}
	s.endpointPools["pool-1"] = endpoints.EndpointPool{ID: "pool-1", GroupID: "group-1", Hostname: "edge.example.test"}
	s.endpointMembers["pool-1"] = map[string]endpoints.EndpointPoolMember{"node-1": {PoolID: "pool-1", GroupID: "group-1", NodeID: "node-1", Weight: 2}}
	s.endpointCreateKeys["group-1\x00create"] = "pool-1"
	s.endpointMemberKeys["pool-1\x00member"] = "node-1"
	s.endpointCreateHashes["group-1\x00create"] = "create-hash"
	s.endpointMemberHashes["pool-1\x00member"] = "member-hash"
	s.revisionsByGroup["group-1"] = []generations.GroupRevision{{ID: "rev-1", GroupID: "group-1", Revision: 1, RequestSHA256: "request-hash", IdempotencyKey: "revision-key", Config: json.RawMessage(`{"inbounds":[]}`)}}
	s.revisionByIdempotency["group-1"] = map[string]string{"revision-key": "rev-1"}
	config := generations.NodeConfigGeneration{ID: "config-1", NodeID: "node-1", Generation: 1, Config: json.RawMessage(`{"version":1}`)}
	s.nodeConfigsByNode["node-1"] = map[int64]generations.NodeConfigGeneration{1: config}
	s.nodeConfigsByRevision["rev-1"] = []generations.NodeConfigGeneration{config}
	s.applyResultsByNode["node-1"] = map[int64]map[string]generations.ApplyResult{1: {"commit": {ID: "apply-1", NodeID: "node-1", Generation: 1, Phase: "commit", Status: "succeeded"}}}
	s.auditEvents = []audit.Event{{ID: "audit-1", Action: "test", Metadata: map[string]any{"count": 1.0}}}
	return s
}

func TestSnapshotPreservesAllIdentityAndRoutingState(t *testing.T) {
	s := snapshotFixture(t)
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("unit-test-bootstrap-password")) {
		t.Fatal("plaintext password persisted")
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := restored.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, roundTrip) {
		t.Fatal("snapshot lost state during round trip")
	}
	admin, err := restored.AdministratorByUsername(context.Background(), "admin")
	if err != nil || !bytes.Equal(admin.PasswordHash, s.adminsByUsername["admin"].PasswordHash) {
		t.Fatal("administrator hash not restored")
	}
	if _, err := restored.SessionByTokenHash(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now()); err != nil {
		t.Fatal("session not restored")
	}
	node, err := restored.NodeByCredentialHash(context.Background(), "credential-hash")
	if err != nil || node.CredentialHash != "credential-hash" {
		t.Fatal("node credential not restored")
	}
	publicNode, _ := json.Marshal(node)
	publicAdmin, _ := json.Marshal(admin)
	publicRevision, _ := json.Marshal(restored.revisionsByGroup["group-1"][0])
	if bytes.Contains(publicNode, []byte("credential-hash")) || bytes.Contains(publicAdmin, admin.PasswordHash) || bytes.Contains(publicRevision, []byte("revision-key")) {
		t.Fatal("private persistence fields exposed through public models")
	}
}

func TestDeviceGroupMetadataSnapshotAndLegacyDefaults(t *testing.T) {
	s := snapshotFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	s.userGroups["ugrp-1"] = customers.UserGroup{ID: "ugrp-1", Name: "operators", Revision: 1, CreatedAt: now, UpdatedAt: now}
	group := s.deviceGroups["group-1"]
	group.UserGroupID, group.HideInProbe = "ugrp-1", true
	s.deviceGroups[group.ID] = group
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.deviceGroups[group.ID]; got.UserGroupID != "ugrp-1" || !got.HideInProbe {
		t.Fatalf("device-group metadata lost in snapshot: %+v", got)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	var deviceGroups map[string]map[string]json.RawMessage
	if err := json.Unmarshal(legacy["DeviceGroups"], &deviceGroups); err != nil {
		t.Fatal(err)
	}
	delete(deviceGroups[group.ID], "user_group_id")
	delete(deviceGroups[group.ID], "hide_in_probe")
	legacy["DeviceGroups"], err = json.Marshal(deviceGroups)
	if err != nil {
		t.Fatal(err)
	}
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyRestored, err := DecodeSnapshot(legacyRaw)
	if err != nil {
		t.Fatal(err)
	}
	if got := legacyRestored.deviceGroups[group.ID]; got.UserGroupID != "" || got.HideInProbe {
		t.Fatalf("legacy snapshot did not default device-group metadata: %+v", got)
	}
	delete(s.userGroups, "ugrp-1")
	invalid, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(invalid); err == nil {
		t.Fatal("snapshot accepted a missing device-group user group")
	}
}

func TestSnapshotRejectsCorruptUnknownOrIncompleteState(t *testing.T) {
	raw, err := snapshotFixture(t).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	unknownVersion := cloneRawMap(encoded)
	unknownVersion["Version"] = json.RawMessage("999")
	unknownField := cloneRawMap(encoded)
	unknownField["Unexpected"] = json.RawMessage("true")
	unknownVersionRaw, err := json.Marshal(unknownVersion)
	if err != nil {
		t.Fatal(err)
	}
	unknownFieldRaw, err := json.Marshal(unknownField)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":              nil,
		"malformed":          []byte(`{"Version":`),
		"unknown version":    unknownVersionRaw,
		"missing maps":       []byte(`{"Version":1}`),
		"unknown field":      unknownFieldRaw,
		"trailing":           append(append([]byte(nil), raw...), []byte(`{}`)...),
		"oversized":          []byte(strings.Repeat(" ", MaxSnapshotBytes+1)),
		"missing credential": bytes.Replace(raw, []byte(`"CredentialHash":"credential-hash"`), []byte(`"CredentialHash":""`), 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSnapshot(data); err == nil {
				t.Fatal("unsafe snapshot accepted")
			}
		})
	}
}

func TestSnapshotRejectsAdministratorSessionsWithBrokenIdentityBinding(t *testing.T) {
	raw, err := snapshotFixture(t).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	base := func(t *testing.T) snapshot {
		t.Helper()
		var state snapshot
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}

	cases := map[string]func(snapshot){
		"unknown administrator": func(state snapshot) {
			hash := strings.Repeat("f", 64)
			state.Sessions[hash] = auth.Session{
				ID: "forged-session", AdminID: "missing-admin", TokenHash: hash,
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
		},
		"token hash does not match map key": func(state snapshot) {
			mapKey := strings.Repeat("b", 64)
			state.Sessions[mapKey] = auth.Session{
				ID: "forged-session", AdminID: "adm-1", TokenHash: strings.Repeat("c", 64),
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
		},
		"token hash is not hexadecimal": func(state snapshot) {
			hash := strings.Repeat("g", 64)
			state.Sessions[hash] = auth.Session{
				ID: "forged-session", AdminID: "adm-1", TokenHash: hash,
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
		},
		"token hash is uppercase": func(state snapshot) {
			hash := strings.Repeat("A", 64)
			state.Sessions[hash] = auth.Session{
				ID: "forged-session", AdminID: "adm-1", TokenHash: hash,
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
		},
		"blank session identity": func(state snapshot) {
			state.Sessions["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"] = auth.Session{
				ID: " ", AdminID: "adm-1", TokenHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
		},
		"invalid time range": func(state snapshot) {
			hash := strings.Repeat("9", 64)
			state.Sessions[hash] = auth.Session{
				ID: "forged-session", AdminID: "adm-1", TokenHash: hash,
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(-time.Hour),
			}
		},
		"zero creation time": func(state snapshot) {
			state.Sessions["cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"] = auth.Session{
				ID: "forged-session", AdminID: "adm-1", TokenHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
		},
		"duplicate session identity": func(state snapshot) {
			hash := strings.Repeat("e", 64)
			state.Sessions[hash] = auth.Session{
				ID: "session-1", AdminID: "adm-1", TokenHash: hash,
				CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			state := base(t)
			mutate(state)
			mutated, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(mutated); err == nil {
				t.Fatal("DecodeSnapshot accepted an administrator session with an invalid binding")
			}
		})
	}
}

func TestSnapshotAcceptsExpiredAdministratorSessionsForCompatibility(t *testing.T) {
	state := snapshotFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	state.sessionsByHash["dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"] = auth.Session{
		ID: "expired-session", AdminID: "adm-1", TokenHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	raw, err := state.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("expired but structurally valid session rejected: %v", err)
	}
	if _, err := restored.SessionByTokenHash(context.Background(), "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", now); err == nil {
		t.Fatal("expired administrator session remained usable after restore")
	}
}

func TestSnapshotVersionThreeLoadsWithEmptyRuleGroups(t *testing.T) {
	raw, err := snapshotFixture(t).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["Version"] = float64(networkPolicySnapshotVersion)
	delete(legacy, "RuleGroups")
	delete(legacy, "SiteSettings")
	delete(legacy, "Announcements")
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("legacy v3 snapshot rejected: %v", err)
	}
	if groups, err := restored.ListRuleGroups(context.Background()); err != nil || len(groups) != 0 {
		t.Fatalf("legacy groups: %v %+v", err, groups)
	}
	upgraded, err := restored.EncodeSnapshot()
	if err != nil || !bytes.Contains(upgraded, []byte(fmt.Sprintf(`"Version":%d`, SnapshotVersion))) || !bytes.Contains(upgraded, []byte(`"RuleGroups":{}`)) || !bytes.Contains(upgraded, []byte(`"Announcements":{}`)) || !bytes.Contains(upgraded, []byte(`"CustomerSessions":{}`)) {
		t.Fatalf("legacy snapshot was not upgraded: %v", err)
	}
}

func cloneRawMap(source map[string]json.RawMessage) map[string]json.RawMessage {
	clone := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		clone[key] = append(json.RawMessage(nil), value...)
	}
	return clone
}
