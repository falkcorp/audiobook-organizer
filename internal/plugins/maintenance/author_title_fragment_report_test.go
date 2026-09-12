// file: internal/plugins/maintenance/author_title_fragment_report_test.go
// version: 1.0.0
// guid: 06a545b9-43a8-4225-b093-be21b4311373
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// titleFragmentMutations records every author mutation the op attempts. The op is
// report-only, so the assertion that matters is SILENCE here, not a count.
type titleFragmentMutations struct {
	created, renamed, deleted int
}

func newTitleFragmentPlugin(authors []database.Author, books map[int]int, booksErr error, muts *titleFragmentMutations) *Plugin {
	store := &database.MockStore{
		GetAllAuthorsFunc:          func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return books, booksErr },
		CreateAuthorFunc: func(name string) (*database.Author, error) {
			muts.created++
			return &database.Author{Name: name}, nil
		},
		UpdateAuthorNameFunc: func(int, string) error { muts.renamed++; return nil },
		DeleteAuthorFunc:     func(int) error { muts.deleted++; return nil },
	}
	return &Plugin{deps: &fakeDeps{store: store}}
}

// runTitleFragmentOp drives the real Run function end to end and returns the
// activity-log lines it wrote.
func runTitleFragmentOp(t *testing.T, authors []database.Author, books map[int]int) []string {
	t.Helper()
	var muts titleFragmentMutations
	p := newTitleFragmentPlugin(authors, books, nil, &muts)
	rep := &fakeReporter{}
	if err := p.runAuthorTitleFragmentScan(context.Background(), nil, rep); err != nil {
		t.Fatalf("runAuthorTitleFragmentScan: %v", err)
	}
	if muts != (titleFragmentMutations{}) {
		t.Fatalf("report-only op mutated authors: %+v", muts)
	}
	return rep.logs
}

func flaggedLines(logs []string) []string {
	var out []string
	for _, l := range logs {
		if strings.HasPrefix(l, "flagged author ") {
			out = append(out, l)
		}
	}
	return out
}

func TestAuthorTitleFragmentScan_FlagsHyphenPrefixed(t *testing.T) {
	// "-" alone is the 1-char edge case: flagged, no panic.
	for _, name := range []string{"-Something", "- Edgedancer", "-", "  - The Emperor's Soul"} {
		if got := classifyTitleFragmentAuthor(name); got != titleFragmentReasonLeadingHyphen {
			t.Errorf("classify(%q) = %q, want %q", name, got, titleFragmentReasonLeadingHyphen)
		}
	}
	r := scanTitleFragmentAuthors([]database.Author{{ID: 7, Name: "- Edgedancer"}}, map[int]int{7: 0}, 100)
	if r.LeadingHyphen != 1 || r.Flagged != 1 || len(r.HyphenSample) != 1 || r.HyphenSample[0].ID != 7 {
		t.Fatalf("hyphen row not reported: %+v", r)
	}
}

func TestAuthorTitleFragmentScan_FlagsNonPersonShape(t *testing.T) {
	for _, name := range []string{"04 - Heir to the Jedi", "Do Androids Dream?", "the long earth", "Plato"} {
		if got := classifyTitleFragmentAuthor(name); got != titleFragmentReasonNotPersonName {
			t.Errorf("classify(%q) = %q, want %q", name, got, titleFragmentReasonNotPersonName)
		}
	}
}

// Anti-over-suppression: real names, including a lowercase particle, stay clean.
func TestAuthorTitleFragmentScan_DoesNotFlagRealNames(t *testing.T) {
	for _, name := range []string{"Ludwig van Beethoven", "Brandon Sanderson", "Ursula K. Le Guin", "Kevin J Anderson"} {
		if got := classifyTitleFragmentAuthor(name); got != "" {
			t.Errorf("classify(%q) = %q, want not flagged", name, got)
		}
	}
}

// Blank names must not flag (LooksLikePersonName("") is false) but must be counted.
func TestAuthorTitleFragmentScan_BlankNamesCountedNotFlagged(t *testing.T) {
	r := scanTitleFragmentAuthors([]database.Author{{ID: 1, Name: ""}, {ID: 2, Name: "   "}}, nil, 100)
	if r.Flagged != 0 || r.Blank != 2 || r.TotalAuthors != 2 {
		t.Fatalf("blank handling wrong: %+v", r)
	}
}

// Seeded corpus: 3 known-bad + 3 known-good through the real Run path reports
// exactly 3 flagged rows, with their book counts, and mutates nothing.
func TestAuthorTitleFragmentScan_SeededCorpusFlagsExactlyThree(t *testing.T) {
	authors := []database.Author{
		{ID: 1, Name: "Brandon Sanderson"},
		{ID: 2, Name: "- Edgedancer"},
		{ID: 3, Name: "Ludwig van Beethoven"},
		{ID: 4, Name: "04 - Heir to the Jedi"},
		{ID: 5, Name: "Karen Joy Fowler"},
		{ID: 6, Name: "-"},
	}
	books := map[int]int{1: 12, 2: 0, 3: 4, 4: 2, 5: 3, 6: 0}
	logs := runTitleFragmentOp(t, authors, books)

	got := flaggedLines(logs)
	if len(got) != 3 {
		t.Fatalf("flagged %d rows, want 3: %q", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"author_id=2 books=0", "author_id=4 books=2", "author_id=6 books=0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in flagged rows:\n%s", want, joined)
		}
	}
	if !containsLine(logs, "flagged=3 leading-hyphen=2 not-person-shape=1") {
		t.Errorf("summary line wrong: %q", logs)
	}
}

// The cap applies per reason, so the zero-book hyphen rows this op exists for
// still appear even when more than the cap of shape failures have books.
func TestAuthorTitleFragmentScan_SampleCapPerReason(t *testing.T) {
	var authors []database.Author
	books := map[int]int{}
	for i := 1; i <= titleFragmentSampleLimit+5; i++ {
		authors = append(authors, database.Author{ID: i, Name: fmt.Sprintf("Track %03d", i)})
		books[i] = 10
	}
	for i := 1000; i < 1003; i++ {
		authors = append(authors, database.Author{ID: i, Name: fmt.Sprintf("- Chapter %d", i)})
	}
	r := scanTitleFragmentAuthors(authors, books, titleFragmentSampleLimit)
	if r.NotPersonName != titleFragmentSampleLimit+5 || r.LeadingHyphen != 3 {
		t.Fatalf("counts must cover every row, not the sample: %+v", r)
	}
	if len(r.NotPersonSample) != titleFragmentSampleLimit {
		t.Errorf("not-person sample = %d rows, want cap %d", len(r.NotPersonSample), titleFragmentSampleLimit)
	}
	if len(r.HyphenSample) != 3 {
		t.Errorf("hyphen sample = %d rows, want 3 (crowded out by the other reason?)", len(r.HyphenSample))
	}

	logs := runTitleFragmentOp(t, authors, books)
	if n := len(flaggedLines(logs)); n != titleFragmentSampleLimit+3 {
		t.Errorf("logged %d flagged rows, want %d", n, titleFragmentSampleLimit+3)
	}
}

func TestAuthorTitleFragmentScan_EmptyAuthorList(t *testing.T) {
	logs := runTitleFragmentOp(t, nil, map[int]int{})
	if n := len(flaggedLines(logs)); n != 0 {
		t.Fatalf("empty library flagged %d rows", n)
	}
	if !containsLine(logs, "authors=0 flagged=0") {
		t.Errorf("summary missing for empty library: %q", logs)
	}
}

// A book-count read failure must fail the op, not render every row as "0 books".
func TestAuthorTitleFragmentScan_BookCountErrorFailsOp(t *testing.T) {
	var muts titleFragmentMutations
	p := newTitleFragmentPlugin([]database.Author{{ID: 1, Name: "- X"}}, nil, errors.New("boom"), &muts)
	if err := p.runAuthorTitleFragmentScan(context.Background(), nil, &fakeReporter{}); err == nil {
		t.Fatal("expected error when book counts cannot be read")
	}
}

// Read-only and unscheduled by construction.
func TestAuthorTitleFragmentScan_DefIsReadOnlyAndManual(t *testing.T) {
	def := (&Plugin{}).authorTitleFragmentScanDef()
	if def.Schedule != nil {
		t.Errorf("op must not be scheduled, got %q", *def.Schedule)
	}
	if len(def.Capabilities) != 1 || def.Capabilities[0] != sdk.CapLibraryRead {
		t.Errorf("capabilities = %v, want only CapLibraryRead", def.Capabilities)
	}
	if def.Liveness != sdk.LivenessManual {
		t.Errorf("liveness = %v, want LivenessManual", def.Liveness)
	}
}

func containsLine(logs []string, sub string) bool {
	for _, l := range logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
