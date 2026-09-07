// file: internal/plugins/maintenance/clear_apply_rename_failures_test.go
// version: 1.0.0
// guid: 7d5b2e09-3f41-4c86-95a2-0be7143d8c66
// last-edited: 2026-09-07

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// newClearFailuresPlugin wires a MockStore over a fixed `_system` preference
// set, recording every write so a dry run can be asserted on SILENCE rather
// than on a return value.
func newClearFailuresPlugin(prefs map[string]string, writes *map[string]string) *Plugin {
	store := &database.MockStore{
		GetAllPreferencesForUserFunc: func(userID string) ([]database.UserPreferenceKV, error) {
			out := make([]database.UserPreferenceKV, 0, len(prefs))
			for k, v := range prefs {
				out = append(out, database.UserPreferenceKV{UserID: userID, Key: k, Value: v})
			}
			return out, nil
		},
		SetUserPreferenceForUserFunc: func(_, key, value string) error {
			(*writes)[key] = value
			return nil
		},
	}
	return &Plugin{deps: &fakeDeps{store: store}}
}

func recordJSON(t *testing.T, bookID, target, reason string) string {
	t.Helper()
	b, err := json.Marshal(organizer.ApplyRenameFailure{
		BookID: bookID, TargetPath: target, OccupantPath: target, Reason: reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClearApplyRenameFailures(t *testing.T) {
	fixture := func(t *testing.T) map[string]string {
		return map[string]string{
			organizer.ApplyRenameFailurePrefix + "b1": recordJSON(t, "b1", "/lib/A.m4b", "occupied"),
			organizer.ApplyRenameFailurePrefix + "b2": recordJSON(t, "b2", "/lib/B.m4b", "occupied"),
			// Already cleared — the blank-value convention. Must not be
			// counted or re-written.
			organizer.ApplyRenameFailurePrefix + "b3": "",
			// A checkpoint in the same namespace. Must not be touched.
			"pipeline_checkpoint:b4:rename": "2026-09-07T00:00:00Z",
		}
	}

	t.Run("dry run reports and writes nothing", func(t *testing.T) {
		writes := map[string]string{}
		p := newClearFailuresPlugin(fixture(t), &writes)
		if err := p.runClearApplyRenameFailures(context.Background(), nil, &fakeReporter{}); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(writes) != 0 {
			t.Fatalf("a dry run must not write: %v", writes)
		}
	})

	t.Run("apply clears only the live failure records", func(t *testing.T) {
		writes := map[string]string{}
		p := newClearFailuresPlugin(fixture(t), &writes)
		params, _ := json.Marshal(clearApplyRenameFailuresParams{Apply: true})
		if err := p.runClearApplyRenameFailures(context.Background(), params, &fakeReporter{}); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(writes) != 2 {
			t.Fatalf("expected exactly the two live records to be cleared, got %v", writes)
		}
		for _, k := range []string{organizer.ApplyRenameFailurePrefix + "b1", organizer.ApplyRenameFailurePrefix + "b2"} {
			v, ok := writes[k]
			if !ok || v != "" {
				t.Fatalf("%s was not blanked: %q (present=%t)", k, v, ok)
			}
		}
		if _, ok := writes["pipeline_checkpoint:b4:rename"]; ok {
			t.Fatal("the op must not touch pipeline checkpoints")
		}
	})

	t.Run("book_ids scopes the clear", func(t *testing.T) {
		writes := map[string]string{}
		p := newClearFailuresPlugin(fixture(t), &writes)
		params, _ := json.Marshal(clearApplyRenameFailuresParams{Apply: true, BookIDs: []string{"b2"}})
		if err := p.runClearApplyRenameFailures(context.Background(), params, &fakeReporter{}); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(writes) != 1 {
			t.Fatalf("expected only b2 to be cleared, got %v", writes)
		}
		if _, ok := writes[organizer.ApplyRenameFailurePrefix+"b2"]; !ok {
			t.Fatalf("b2 was not the record cleared: %v", writes)
		}
	})

	t.Run("an unreadable record is still clearable", func(t *testing.T) {
		writes := map[string]string{}
		p := newClearFailuresPlugin(map[string]string{
			organizer.ApplyRenameFailurePrefix + "b9": "{not json",
		}, &writes)
		params, _ := json.Marshal(clearApplyRenameFailuresParams{Apply: true})
		if err := p.runClearApplyRenameFailures(context.Background(), params, &fakeReporter{}); err != nil {
			t.Fatalf("run: %v", err)
		}
		if _, ok := writes[organizer.ApplyRenameFailurePrefix+"b9"]; !ok {
			t.Fatalf("a record that cannot be decoded is exactly the stuck state this op exists for: %v", writes)
		}
	})
}
