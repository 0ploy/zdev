package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/runtime"
)

// TestRename_RefusesWhenTargetVolumeExists: a `zdev down`ed project of the
// new name keeps its volumes; copying into them would overlay two datasets
// and then delete the source. The refusal must come before teardown.
func TestRename_RefusesWhenTargetVolumeExists(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)
	mock.VolumesExist[p.VolumeName("data")] = true
	mock.VolumesExist[VolumeNameFor("data", "renamed")] = true

	err := p.Rename(context.Background(), "renamed")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Rename() error = %v, want an 'already exists' refusal", err)
	}
	for _, method := range []string{"StopContainer", "RemoveContainer", "CopyVolume", "RemoveVolume"} {
		if n := mock.CallCount(method); n != 0 {
			t.Errorf("%s called %d times before the refusal, want 0", method, n)
		}
	}
}

// TestRename_VolumeLookupErrorAborts: a transient Docker error used to read
// as "no such volume", so rename completed and the project restarted empty.
func TestRename_VolumeLookupErrorAborts(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)
	mock.Errors["VolumeExists"] = errors.New("daemon hiccup")

	if err := p.Rename(context.Background(), "renamed"); err == nil {
		t.Fatal("Rename() succeeded despite a failing volume lookup")
	}
	if n := mock.CallCount("RemoveContainer"); n != 0 {
		t.Errorf("RemoveContainer called %d times, want 0 (abort before teardown)", n)
	}
}

// TestRename_LateFailureKeepsOldVolumes: old volumes used to be removed
// before the network/state/config steps, so a failure there left config and
// state on the old name with its data gone.
func TestRename_LateFailureKeepsOldVolumes(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)
	oldVol := p.VolumeName("data")
	newVol := VolumeNameFor("data", "renamed")
	mock.VolumesExist[oldVol] = true
	mock.NetworksExist[p.NetworkName()] = true
	mock.Errors["RemoveNetwork"] = errors.New("network has active endpoints")

	if err := p.Rename(context.Background(), "renamed"); err == nil {
		t.Fatal("Rename() succeeded despite a failing network removal")
	}
	if !mock.VolumesExist[oldVol] {
		t.Errorf("old volume %s was removed although the rename failed", oldVol)
	}
	if mock.VolumesExist[newVol] {
		t.Errorf("copied volume %s was not rolled back, a retry would be refused", newVol)
	}
}
