// file: internal/metafetch/apply_collision_failure_test.go
// version: 1.0.0
// guid: 2f8c05a4-6b17-49de-83b0-5a1e7c94d20f
// last-edited: 2026-09-07

package metafetch

import (
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

type prefRecorder struct {
	kv map[string]string
}

func newPrefRecorder() *prefRecorder { return &prefRecorder{kv: map[string]string{}} }

func (p *prefRecorder) GetUserPreference(string) (*database.UserPreference, error) { return nil, nil }
func (p *prefRecorder) SetUserPreference(string, string) error                     { return nil }
func (p *prefRecorder) DeleteUserPreference(string) error                          { return nil }
func (p *prefRecorder) GetAllUserPreferences() ([]database.UserPreference, error)  { return nil, nil }
func (p *prefRecorder) SetUserPreferenceForUser(_, key, value string) error {
	p.kv[key] = value
	return nil
}

func (p *prefRecorder) GetUserPreferenceForUser(userID, key string) (*database.UserPreferenceKV, error) {
	v, ok := p.kv[key]
	if !ok {
		return nil, nil
	}
	return &database.UserPreferenceKV{UserID: userID, Key: key, Value: v}, nil
}

func (p *prefRecorder) GetAllPreferencesForUser(string) ([]database.UserPreferenceKV, error) {
	return nil, nil
}

// TestRecordRenameCollisionFailure_OnlyRecordsCollisions is the boundary that
// keeps a transient outage from turning into a library-wide skip list. A
// stranded temp, a NAS blip or a permissions error can all succeed on a re-run
// with no change to the world, so they keep the old retry-next-run behaviour;
// only an unresolvable collision earns a durable record.
func TestRecordRenameCollisionFailure_OnlyRecordsCollisions(t *testing.T) {
	key := organizer.ApplyRenameFailurePrefix + "b1"

	t.Run("a plain rename error is not recorded", func(t *testing.T) {
		prefs := newPrefRecorder()
		recordRenameCollisionFailure(prefs, "b1", &RenameResult{},
			fmt.Errorf("rename /x -> temp: %w", errors.New("input/output error")))
		if _, ok := prefs.kv[key]; ok {
			t.Fatal("a transient rename failure must stay retryable")
		}
	})

	t.Run("a wrapped collision error is recorded with the occupant fingerprint", func(t *testing.T) {
		prefs := newPrefRecorder()
		cerr := &organizer.CollisionError{Failures: []organizer.CollisionFailure{{
			SegmentID:       "f1",
			SourcePath:      "/lib/in/a.m4b",
			TargetPath:      "/lib/Author/Title.m4b",
			OccupantPath:    "/lib/Author/Title.m4b",
			OccupantSize:    4096,
			OccupantModUnix: 1757000000,
			Reason:          "byte-identical occupant but the repoint failed",
		}}}
		recordRenameCollisionFailure(prefs, "b1", &RenameResult{}, fmt.Errorf("rename files: %w", cerr))

		rec, ok := organizer.LoadApplyRenameFailure(prefs, "b1")
		if !ok {
			t.Fatal("an unresolvable collision must produce a durable record")
		}
		if rec.TargetPath != "/lib/Author/Title.m4b" || rec.OccupantSize != 4096 || rec.OccupantModUnix != 1757000000 {
			t.Fatalf("record lost the fingerprint the self-heal needs: %+v", rec)
		}
		if rec.RecordedAt == "" {
			t.Fatal("record has no timestamp")
		}
	})
}

func TestTargetPathsOf(t *testing.T) {
	got := targetPathsOf([]FileRenameEntry{
		{TargetPath: "/a.m4b"},
		{TargetPath: "/b.m4b"},
	})
	if len(got) != 2 || got[0] != "/a.m4b" || got[1] != "/b.m4b" {
		t.Fatalf("targetPathsOf = %v", got)
	}
}
