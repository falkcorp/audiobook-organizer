// file: internal/plugins/maintenance/duration_backfill_slots_test.go
// version: 1.0.0
// guid: 971a9404-c6e2-4b74-9aa9-4ba77e70ac10
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The rows of a version slot (.versions/<id>/, a trashed or alternate
// version's files) are not counted by the sums, so normal mode does not measure
// them either: with no copy in the book the total it measured used to include
// them, and the book read as "would change" on every run against the total
// RecomputeBookAggregates stores.
func TestDurationBackfill_NormalModeSkipsVersionSlotRows(t *testing.T) {
	var segWrites, bookWrites int
	segs := []database.BookFile{
		{ID: "01", BookID: "b1", FilePath: "/srv/library/Gotham/Batman Hush/01.m4a", FileSize: 50_000_000, Duration: 0, AcoustIDFingerprintDurationSec: 3600.0},
		{ID: "alt", BookID: "b1", VersionID: "v-alt", FilePath: "/srv/library/Gotham/Batman Hush/.versions/v-alt/01.m4a", FileSize: 51_000_000, Duration: 0, AcoustIDFingerprintDurationSec: 3700.0},
	}
	store := bookWithSegs(t, segs, &segWrites, &bookWrites)
	book := database.Book{ID: "b1", Title: "Batman: Hush", FilePath: "/srv/library/Gotham/Batman Hush", Duration: new(3000)}
	res := processBookForReextractMode(context.Background(), store, book, time.Time{}, false, &readWarnLimiter{})
	if want := 3600; res.newDur != want {
		t.Errorf("newDur = %d, want the live row's %d (the alt slot row is not counted)", res.newDur, want)
	}
	var changed []string
	for _, f := range res.changedBFs {
		changed = append(changed, f.ID)
	}
	if len(changed) != 1 || changed[0] != "01" {
		t.Errorf("changed rows = %v, want [01] (never the slot row)", changed)
	}
}
