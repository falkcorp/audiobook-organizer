// file: internal/operations/registry/reporter_db_noop_internal_test.go
// version: 1.0.0
// guid: 3f8b2c61-9d4e-4a7f-b015-6e2d8c9a4b17
// last-edited: 2026-10-15

package registry

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type p6CapturingRecorder struct{ got []database.ActivityEntry }

func (c *p6CapturingRecorder) Record(e database.ActivityEntry) error {
	c.got = append(c.got, e)
	return nil
}

// TestRecordActivity_NoOpAttrAddsNoOpTag: a line logged with NoOpAttr carries
// activity.NoOpTag, so the feed hides it by default; a line without it does not.
func TestRecordActivity_NoOpAttrAddsNoOpTag(t *testing.T) {
	rec := &p6CapturingRecorder{}
	r := &dbReporter{opID: "op-1", defID: "maintenance.example", plugin: "maintenance", activityRecorder: rec}

	r.recordActivity(logEntry{level: slog.LevelInfo, message: "already complete", attrs: []slog.Attr{NoOpAttr()}})
	r.recordActivity(logEntry{level: slog.LevelInfo, message: "did work"})
	r.recordActivity(logEntry{level: slog.LevelInfo, message: "explicit false", attrs: []slog.Attr{slog.Bool(NoOpAttrKey, false)}})

	if len(rec.got) != 3 {
		t.Fatalf("recorded %d entries, want 3", len(rec.got))
	}
	if !slices.Contains(rec.got[0].Tags, activity.NoOpTag) {
		t.Errorf("no-op line tags = %v, want %q among them", rec.got[0].Tags, activity.NoOpTag)
	}
	for _, e := range rec.got[1:] {
		if slices.Contains(e.Tags, activity.NoOpTag) {
			t.Errorf("line %q tags = %v, must not carry %q", e.Summary, e.Tags, activity.NoOpTag)
		}
	}
}
