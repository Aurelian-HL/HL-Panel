package memoryrepo

import (
	"context"
	"errors"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/groups"
	"testing"
	"time"
)

func TestEnrollmentDialAddressPersistsAndRecoveryRejectsChangedAddress(t *testing.T) {
	ctx := context.Background()
	store := snapshotFixture(t)
	now := time.Now().UTC()
	event, err := audit.NewEvent(now, "administrator", "admin", "test", "device_group", "dial-group", "succeeded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "dial-group", Name: "dial", Kind: groups.KindEntry, CreatedAt: now, UpdatedAt: now}, event); err != nil {
		t.Fatal(err)
	}
	service := enrollment.NewService(store, func() time.Time { return now }, time.Hour)
	issued, err := service.IssueForGroup(ctx, "admin", "address-test", "dial-group", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := enrollment.EnrollInput{RawToken: issued.Token, Hostname: "internal-host", DialHost: "Node.Example.COM.", Platform: "linux", Architecture: "amd64", AgentVersion: "test", EnrollmentSecret: "1234567890123456789012345678901234567890123456789012345678901234"}
	result, err := service.Enroll(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	members, err := store.ListGroupMembers(ctx, "dial-group")
	if err != nil || len(members) != 1 || members[0].DialHost != "node.example.com" {
		t.Fatalf("members=%+v err=%v", members, err)
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	recovered := enrollment.NewService(restored, func() time.Time { return now }, time.Hour)
	replay, err := recovered.Enroll(ctx, input)
	if err != nil || replay.NodeID != result.NodeID {
		t.Fatalf("recovery=%+v err=%v", replay, err)
	}
	input.DialHost = "another.example.com"
	if _, err = recovered.Enroll(ctx, input); !errors.Is(err, faults.ErrAlreadyUsed) {
		t.Fatalf("address changed recovery=%v", err)
	}
	input.DialHost = "https://bad.example.com:443/"
	if _, err = recovered.Enroll(ctx, input); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("invalid address=%v", err)
	}
}
