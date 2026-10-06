package groups

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
)

type recordingRepository struct {
	created      []DeviceGroup
	weightUpdate UpdateMemberWeightInput
}

func (r *recordingRepository) CreateDeviceGroup(_ context.Context, group DeviceGroup, _ audit.Event) error {
	r.created = append(r.created, group)
	return nil
}

func (*recordingRepository) ListDeviceGroups(context.Context) ([]DeviceGroup, error) {
	return nil, nil
}

func (*recordingRepository) UpdateDeviceGroup(context.Context, UpdateInput, audit.Event) (DeviceGroup, bool, error) {
	return DeviceGroup{}, false, nil
}

func (*recordingRepository) DeleteDeviceGroup(context.Context, DeleteInput, audit.Event) (bool, error) {
	return false, nil
}

func (*recordingRepository) ListGroupMembers(context.Context, string) ([]Member, error) {
	return nil, nil
}

func (*recordingRepository) UpsertGroupMember(context.Context, Member, audit.Event) (Member, []generations.NodeConfigGeneration, error) {
	return Member{}, nil, nil
}

func (r *recordingRepository) UpdateGroupMemberWeight(_ context.Context, input UpdateMemberWeightInput, _ audit.Event) (UpdateMemberWeightResult, error) {
	r.weightUpdate = input
	return UpdateMemberWeightResult{}, nil
}

func TestUpdateMemberWeightAcceptsZeroAndRejectsInvalidWeight(t *testing.T) {
	repository := &recordingRepository{}
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	service := NewService(repository, func() time.Time { return now.Add(time.Minute) })
	if _, err := service.UpdateMemberWeight(context.Background(), "admin", "group", "node", 0, now, "key"); err != nil {
		t.Fatal(err)
	}
	if repository.weightUpdate.Weight != 0 || !repository.weightUpdate.ExpectedUpdatedAt.Equal(now) || repository.weightUpdate.RequestSHA256 == "" {
		t.Fatalf("invalid weight update input: %+v", repository.weightUpdate)
	}
	for _, weight := range []int{-1, 1001} {
		if _, err := service.UpdateMemberWeight(context.Background(), "admin", "group", "node", weight, now, "key"); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("weight %d: got %v", weight, err)
		}
	}
}

func (*recordingRepository) RetireGroupMember(context.Context, string, string, time.Time, audit.Event) (RetireMemberResult, error) {
	return RetireMemberResult{}, nil
}

func TestCreateOnlyAcceptsEntryAndExitKinds(t *testing.T) {
	repository := &recordingRepository{}
	service := NewService(repository, nil)

	for _, kind := range []Kind{KindEdge, KindHybrid} {
		if _, err := service.Create(context.Background(), "admin", "legacy", kind, endpoints.SelectionWeightedRoundRobin, ""); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("expected %s to be rejected as a legacy kind, got %v", kind, err)
		}
	}

	for _, kind := range []Kind{KindEntry, KindExit} {
		if _, err := service.Create(context.Background(), "admin", string(kind), kind, endpoints.SelectionWeightedRoundRobin, ""); err != nil {
			t.Fatalf("expected %s to be creatable: %v", kind, err)
		}
	}
	if len(repository.created) != 2 {
		t.Fatalf("expected two created groups, got %d", len(repository.created))
	}
}

func TestCreateWithMetadataPreservesDeviceGroupFields(t *testing.T) {
	repository := &recordingRepository{}
	service := NewService(repository, nil)
	group, err := service.CreateWithMetadata(context.Background(), "admin", " entry ", KindEntry, endpoints.SelectionWeightedRoundRobin, "note", CreateMetadata{
		UserGroupID: " ugrp-1 ", HideInProbe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if group.UserGroupID != "ugrp-1" || !group.HideInProbe || len(repository.created) != 1 || repository.created[0] != group {
		t.Fatalf("metadata lost during creation: %+v", group)
	}
}
