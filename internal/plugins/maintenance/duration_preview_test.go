// file: internal/plugins/maintenance/duration_preview_test.go
// version: 1.0.0
// guid: 4a7d92f1-6c38-4b25-9e07-1d84f5b3ce62
// last-edited: 2026-09-21

package maintenance

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// capturingReporter records every progress message so a test can assert on the
// summary a dry run actually shows the operator.
type capturingReporter struct {
	fakeReporter
	msgs []string
}

func (c *capturingReporter) UpdateProgress(_, _ int, message string) error {
	c.msgs = append(c.msgs, message)
	return nil
}

func (c *capturingReporter) last() string {
	if len(c.msgs) == 0 {
		return ""
	}
	return c.msgs[len(c.msgs)-1]
}

// TestDurationBackfill_DryRunReportsWithheldBooks pins that a PREVIEW previews
// the behaviour that matters. incomplete++ used to sit only in the apply path,
// past the `if dryRun { continue }` guard, so a dry run always printed
// incomplete-books=0 no matter how many books would have their total withheld.
//
// That made the preview actively misleading: a sample line reading
// "36229s→1713s (36 seg)" is produced both by a book that really is shorter and
// by a book where 35 of its 36 segments could not be read, and the one number
// that separates those cases read 0 in both.
func TestDurationBackfill_DryRunReportsWithheldBooks(t *testing.T) {
	var segWrites, bookWrites int
	segs := []database.BookFile{
		{ID: "s1", BookID: "b1", FilePath: "/lib/B/01.m4b", Duration: 50, AcoustIDFingerprintDurationSec: 1800.0},
		{ID: "s2", BookID: "b1", FilePath: "/nonexistent/02.m4b", Duration: 50},
		{ID: "s3", BookID: "b1", FilePath: "", Duration: 0},
	}
	store := bookWithSegs(t, segs, &segWrites, &bookWrites)
	rep := &capturingReporter{}

	p := New(fakeDeps{store: store})
	// dryRun = true
	if err := p.runDurationBackfill(context.Background(), mustReextractParams(t, true, 0), rep); err != nil {
		t.Fatalf("run: %v", err)
	}

	if segWrites != 0 || bookWrites != 0 {
		t.Fatalf("a dry run must write nothing, got segWrites=%d bookWrites=%d", segWrites, bookWrites)
	}
	summary := rep.last()
	if strings.Contains(summary, "incomplete-books=0") {
		t.Errorf("dry run reported incomplete-books=0 for a book with 2 unresolved segments; summary: %s", summary)
	}
	if !strings.Contains(summary, "incomplete-books=1") {
		t.Errorf("want incomplete-books=1 in the dry-run summary, got: %s", summary)
	}
}

// TestDurationBackfill_ExampleNamesUnresolvedSegments pins that the sample line
// says WHY a total dropped, so a withheld book cannot be misread as a measured
// correction.
func TestDurationBackfill_ExampleNamesUnresolvedSegments(t *testing.T) {
	var segWrites, bookWrites int
	// s2 has Duration 0, so it cannot take the stored-duration fast path (a
	// non-iTunes segment with a known Duration is trusted WITHOUT touching
	// disk). It falls through to ffprobe, the file is absent, and it is
	// unresolved — the shape of the reported "Fog of War" book, whose phantom
	// row also carried no duration.
	segs := []database.BookFile{
		{ID: "s1", BookID: "b1", FilePath: "/lib/B/01.m4b", Duration: 50, AcoustIDFingerprintDurationSec: 1800.0},
		{ID: "s2", BookID: "b1", FilePath: "/nonexistent/02.m4b", Duration: 0},
	}
	store := bookWithSegs(t, segs, &segWrites, &bookWrites)
	rep := &capturingReporter{}

	p := New(fakeDeps{store: store})
	if err := p.runDurationBackfill(context.Background(), mustReextractParams(t, true, 0), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	summary := rep.last()
	if !strings.Contains(summary, "unresolved") || !strings.Contains(summary, "withheld") {
		t.Errorf("example must name the unresolved segments and say the total is withheld, got: %s", summary)
	}
}
