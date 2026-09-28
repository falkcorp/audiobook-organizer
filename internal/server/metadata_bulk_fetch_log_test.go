// file: internal/server/metadata_bulk_fetch_log_test.go
// version: 1.0.0
// guid: 8966af00-704c-4a19-99e8-b832e27d9f7c
// last-edited: 2026-09-27

package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// recordingProgress is an operations.ProgressReporter that keeps every log
// line and progress message.
type recordingProgress struct {
	mu       sync.Mutex
	lines    []recordedProgressLine
	messages []string
}

type recordedProgressLine struct{ level, msg string }

func (r *recordingProgress) UpdateProgress(_, _ int, message string) error {
	r.mu.Lock()
	r.messages = append(r.messages, message)
	r.mu.Unlock()
	return nil
}
func (r *recordingProgress) Log(level, message string, _ *string) error {
	r.mu.Lock()
	r.lines = append(r.lines, recordedProgressLine{level, message})
	r.mu.Unlock()
	return nil
}
func (r *recordingProgress) IsCanceled() bool { return false }

func (r *recordingProgress) withPrefix(prefix string) []recordedProgressLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedProgressLine
	for _, l := range r.lines {
		if strings.HasPrefix(l.msg, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func (r *recordingProgress) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 {
		return ""
	}
	return r.messages[len(r.messages)-1]
}

var _ operations.ProgressReporter = (*recordingProgress)(nil)

// TestBulkFetchByIDs_LogsPerBookOutcomes runs the by-ID bulk fetch with no
// providers (every searched book is "not found") over one book per path and
// checks the op log: a line per searched or skipped book, one summary line
// for the untitled books it refused to search, and counts by outcome in the
// final progress message. Chapter fragments are "skipped", no longer folded
// into "not found".
func TestBulkFetchByIDs_LogsPerBookOutcomes(t *testing.T) {
	disableMetadataSourcesForTest(t)

	books := map[string]*database.Book{
		"b-real":  {ID: "b-real", Title: "Book One"},
		"b-frag":  {ID: "b-frag", Title: "06 Chapter 6"},
		"b-empty": {ID: "b-empty", Title: ""},
		"b-ph":    {ID: "b-ph", Title: "Unknown Title"},
	}
	store, _, _ := newFastpathMockStore(books, nil)
	srv := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	rec := &recordingProgress{}

	if _, err := srv.runBulkMetadataFetchForBookIDs(context.Background(), "op-bulk-log",
		[]string{"b-real", "b-frag", "b-empty", "b-ph"}, operations.BulkMetadataFetchParams{}, store, rec); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := rec.withPrefix("no match: "); len(got) != 1 || got[0].level != "info" ||
		!strings.Contains(got[0].msg, `"Book One"`) {
		t.Errorf("no-match lines = %v, want one info line for \"Book One\"", got)
	}
	if got := rec.withPrefix("skipped: "); len(got) != 1 || !strings.Contains(got[0].msg, "chapter fragment") {
		t.Errorf("skipped lines = %v, want one chapter-fragment line", got)
	}
	if got := rec.withPrefix("skipped 2 book(s) with an empty or placeholder title"); len(got) != 1 {
		t.Errorf("untitled summary lines = %d, want 1 (lines: %v)", len(got), rec.lines)
	}
	if want := "complete 2/2 — found 0, not found 1, skipped 1, errors 0"; rec.last() != want {
		t.Errorf("final progress = %q, want %q", rec.last(), want)
	}
}

// TestBulkFetchOutcomeLine_FoundAndError covers the two outcomes the
// provider-less run above cannot reach.
func TestBulkFetchOutcomeLine_FoundAndError(t *testing.T) {
	level, msg := bulkFetchOutcomeLine("Dune", "Frank Herbert", metafetch.ChainOutcome{
		Results:    []metadata.BookMetadata{{Title: "Dune", Author: "Frank Herbert"}, {Title: "Dune Messiah"}},
		SourceName: "Audible",
		Variant:    "Dune",
	}, metafetch.FetchStatusCached)
	want := `found: "Dune" by "Frank Herbert" → 2 candidate(s) from Audible, top "Dune" by "Frank Herbert" via title variant "Dune"`
	if level != "info" || msg != want {
		t.Errorf("found line = (%s) %q, want (info) %q", level, msg, want)
	}

	level, msg = bulkFetchOutcomeLine("Dune", "", metafetch.ChainOutcome{
		Err:       errors.New("429 too many requests\nforged"),
		ErrSource: "Google Books",
	}, metafetch.FetchStatusFetchError)
	if level != "warn" || !strings.HasPrefix(msg, `error: "Dune" — provider Google Books failed, left retryable: 429`) {
		t.Errorf("error line = (%s) %q", level, msg)
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("error line carries a raw newline: %q", msg)
	}
}
