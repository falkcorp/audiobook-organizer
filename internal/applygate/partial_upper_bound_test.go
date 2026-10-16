// file: internal/applygate/partial_upper_bound_test.go
// version: 1.0.0
// guid: 8b4e1d27-5c93-4a60-b7f2-0d6a9e3c1f58
// last-edited: 2026-10-15

package applygate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Synthetic fixtures only.

// ownerCaseRows: two ~4 MB files, only the first measured (8 min). The size
// estimate is ~16 min, the upper bound 48 min (2880 s).
func ownerCaseRows() []database.BookFile {
	return []database.BookFile{
		{ID: "f1", BookID: "b", FileSize: 4_000_000, Duration: 480},
		{ID: "f2", BookID: "b", FileSize: 4_000_000},
	}
}

// tenHourRows: 20 x 30 min chapters at 128 kbps (28.8 MB), 2 measured.
func tenHourRows() []database.BookFile {
	out := make([]database.BookFile, 20)
	for i := range out {
		out[i] = database.BookFile{ID: fmt.Sprintf("c%02d", i), BookID: "b", FileSize: 28_800_000}
		if i < 2 {
			out[i].Duration = 1800
		}
	}
	return out
}

func TestCheckRuntime_PartialUpperBound(t *testing.T) {
	// Book.Duration is the partial known sum, as RecomputeBookAggregates stores.
	ownerRT := database.ComputeBookRuntime(&database.Book{ID: "b", Duration: intp(480)}, ownerCaseRows())
	tenRT := database.ComputeBookRuntime(&database.Book{ID: "b", Duration: intp(3600)}, tenHourRows())
	noSize := ownerCaseRows()
	noSize[1].FileSize = 0
	noSizeRT := database.ComputeBookRuntime(&database.Book{ID: "b", Duration: intp(480)}, noSize)
	upper, ok := ownerRT.EstimatedUpperSeconds()
	if !ok || upper != 2880 {
		t.Fatalf("fixture: owner upper = %d, %v, want 2880", upper, ok)
	}
	tenUpper, _ := tenRT.EstimatedUpperSeconds()

	cases := []struct {
		name       string
		rt         database.BookRuntime
		cand       int
		overwrite  bool
		want       string
		wantReason string
	}{
		{"owner case: 14h42m for ~8 MB is blocked", ownerRT, 14*3600 + 42*60, false, OutcomeBlock, ReasonRuntimeMismatch},
		{"owner case blocks overwriting too", ownerRT, 14*3600 + 42*60, true, OutcomeBlock, ReasonRuntimeMismatch},
		{"boundary: exactly the upper bound is unknown", ownerRT, upper, false, OutcomeUnknown, ""},
		{"boundary: one second over the upper bound blocks", ownerRT, upper + 1, false, OutcomeBlock, ReasonRuntimeMismatch},
		{"a candidate near the estimate is unknown, not blocked", ownerRT, 960, false, OutcomeUnknown, ""},
		{"10h book, 2 of 20 chapters measured: a 10h candidate is unknown", tenRT, 36000, false, OutcomeUnknown, ""},
		{"10h book: 20h is still inside the 3x headroom", tenRT, 72000, false, OutcomeUnknown, ""},
		{"10h book: the bound itself passes", tenRT, tenUpper, false, OutcomeUnknown, ""},
		{"10h book: past the bound blocks", tenRT, tenUpper + 1, false, OutcomeBlock, ReasonRuntimeMismatch},
		{"missing sizes: unchanged, a 14h42m candidate is unknown", noSizeRT, 14*3600 + 42*60, false, OutcomeUnknown, ""},
		{"missing sizes keep the overwrite rule", noSizeRT, 14*3600 + 42*60, true, OutcomeBlock, ReasonRuntimeUnknownOverwrite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := checkRuntime(c.rt, &metafetch.MetadataCandidate{DurationSec: c.cand}, c.overwrite)
			if got.Outcome != c.want || got.Reason != c.wantReason {
				t.Fatalf("checkRuntime = %+v, want outcome %q reason %q", got, c.want, c.wantReason)
			}
		})
	}

	t.Run("the reason names the estimate", func(t *testing.T) {
		got := checkRuntime(ownerRT, &metafetch.MetadataCandidate{DurationSec: 14*3600 + 42*60}, false)
		for _, want := range []string{"1 of 2 files", "about 16 min", "at most 48 min", "882 min"} {
			if !strings.Contains(got.Detail, want) {
				t.Errorf("detail %q does not mention %q", got.Detail, want)
			}
		}
	})
	t.Run("an unknown candidate runtime is never blocked", func(t *testing.T) {
		if got := checkRuntime(ownerRT, &metafetch.MetadataCandidate{}, false); got.Outcome != OutcomeUnknown {
			t.Fatalf("checkRuntime = %+v, want unknown", got)
		}
	})
	t.Run("the existing lower-bound block is kept", func(t *testing.T) {
		// 30 x 30 min with 20 probed and no sizes: at least 10 h; a 5 h candidate blocks.
		rt := database.ComputeBookRuntime(&database.Book{ID: "b", Duration: intp(36000)}, chapters(30, 1800, 20))
		if got := checkRuntime(rt, &metafetch.MetadataCandidate{DurationSec: 18000}, false); got.Outcome != OutcomeBlock {
			t.Fatalf("checkRuntime = %+v, want block", got)
		}
	})
}

// The whole evidence leg: the owner's case is refused as runtime_mismatch and
// the 10 h candidate for the 10 h book is not.
func TestCheckEvidenceInBatch_PartialUpperBound(t *testing.T) {
	book := probeBook("", 480)
	rt := database.ComputeBookRuntime(book, ownerCaseRows())
	long := &metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", DurationSec: 14*3600 + 42*60}
	if v := CheckEvidenceInBatch(book, snap(book), rt, long, false, nil); v.Pass || v.Reason != ReasonRuntimeMismatch {
		t.Fatalf("14h42m for 2 files totalling 8 MB: pass=%v reason=%s, want runtime_mismatch block", v.Pass, v.Reason)
	}

	tenBook := probeBook("", 3600)
	tenRT := database.ComputeBookRuntime(tenBook, tenHourRows())
	ten := &metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", DurationSec: 36000}
	if v := CheckEvidenceInBatch(tenBook, snap(tenBook), tenRT, ten, false, nil); v.Reason == ReasonRuntimeMismatch {
		t.Fatalf("10h candidate for a 10h book with 2 of 20 chapters measured was blocked: %+v", v)
	}
}
