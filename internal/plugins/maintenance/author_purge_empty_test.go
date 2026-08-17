// file: internal/plugins/maintenance/author_purge_empty_test.go
// version: 1.0.0
// guid: b83c47f1-2065-4ade-9c18-31d70f5b62ea
// last-edited: 2026-08-17

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newPurgePlugin wires a MockStore over a fixed author table plus book and file
// counts, recording every DeleteAuthor call so a dry-run test can assert on
// SILENCE rather than on a return value — the only assertion that distinguishes
// "reported correctly" from "deleted anyway".
func newPurgePlugin(authors []database.Author, books, files map[int]int, deleted *[]int) *Plugin {
	store := &database.MockStore{
		GetAllAuthorsFunc:          func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return books, nil },
		GetAllAuthorFileCountsFunc: func() (map[int]int, error) { return files, nil },
		DeleteAuthorFunc: func(id int) error {
			*deleted = append(*deleted, id)
			return nil
		},
	}
	return &Plugin{deps: &fakeDeps{store: store}}
}

// purgeFixture: 1 real author with books, 2 pure-junk (no books, no files), and 1
// zero-book author that HAS files — the ambiguous row the guard exists for.
func purgeFixture() ([]database.Author, map[int]int, map[int]int) {
	authors := []database.Author{
		{ID: 1, Name: "Brandon Sanderson"},
		{ID: 2, Name: "- Edgedancer"},
		{ID: 3, Name: "04 - Heir to the Jedi"},
		{ID: 4, Name: "Has Files But No Books"},
	}
	books := map[int]int{1: 12, 2: 0, 3: 0, 4: 0}
	files := map[int]int{1: 40, 2: 0, 3: 0, 4: 7}
	return authors, books, files
}

func runPurge(t *testing.T, params string, deleted *[]int) {
	t.Helper()
	authors, books, files := purgeFixture()
	p := newPurgePlugin(authors, books, files, deleted)
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	if err := p.runPurgeEmptyAuthors(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("runPurgeEmptyAuthors: %v", err)
	}
}

// 🔴 DRY RUN MUST NOT DELETE. This is the assertion that matters most: the op
// deletes rows from a production library, so the default has to be inert. A test
// that only checked the reported counts would pass even if it deleted everything.
func TestPurgeEmptyAuthors_DryRunDeletesNothing(t *testing.T) {
	var deleted []int
	runPurge(t, "", &deleted)
	if len(deleted) != 0 {
		t.Fatalf("dry run deleted %v — the default must be inert", deleted)
	}
	// And explicitly with apply:false, since a client may send it rather than omit it.
	deleted = nil
	runPurge(t, `{"apply":false}`, &deleted)
	if len(deleted) != 0 {
		t.Fatalf("apply=false deleted %v", deleted)
	}
}

// 🔴 THE GUARD. Author 4 has zero books but seven files — more likely a book that
// lost its junction entry than an empty author. Deleting it makes repairable damage
// permanent, so it must be held back unless explicitly overridden.
func TestPurgeEmptyAuthors_HoldsBackAuthorsWithFiles(t *testing.T) {
	var deleted []int
	runPurge(t, `{"apply":true}`, &deleted)

	got := map[int]bool{}
	for _, id := range deleted {
		got[id] = true
	}
	if got[4] {
		t.Error("deleted author 4, which has 7 files — that is the row the guard exists for")
	}
	if got[1] {
		t.Error("deleted author 1, who has 12 books")
	}
	for _, id := range []int{2, 3} {
		if !got[id] {
			t.Errorf("did NOT delete author %d, which has zero books and zero files", id)
		}
	}
	if len(deleted) != 2 {
		t.Errorf("deleted %v, want exactly authors 2 and 3", deleted)
	}
}

// The override must actually override — otherwise the escape hatch is decorative
// and the 822 real rows can never be cleaned up.
func TestPurgeEmptyAuthors_RequireZeroFilesFalseIncludesThem(t *testing.T) {
	var deleted []int
	runPurge(t, `{"apply":true,"require_zero_files":false}`, &deleted)

	got := map[int]bool{}
	for _, id := range deleted {
		got[id] = true
	}
	if !got[4] {
		t.Error("require_zero_files=false did not include author 4 — the override does nothing")
	}
	if got[1] {
		t.Error("deleted author 1, who has 12 books — no flag should ever allow that")
	}
	if len(deleted) != 3 {
		t.Errorf("deleted %v, want authors 2, 3 and 4", deleted)
	}
}

// A book count of zero is the ONLY thing that makes a row eligible. Pinned
// separately because it is the invariant a future refactor is most likely to break
// while all the flag tests keep passing.
func TestPurgeEmptyAuthors_NeverDeletesAnAuthorWithBooks(t *testing.T) {
	authors := []database.Author{
		{ID: 1, Name: "One Book"},
		{ID: 2, Name: "Zero Books"},
	}
	var deleted []int
	p := newPurgePlugin(authors, map[int]int{1: 1, 2: 0}, map[int]int{1: 0, 2: 0}, &deleted)
	if err := p.runPurgeEmptyAuthors(context.Background(),
		json.RawMessage(`{"apply":true,"require_zero_files":false}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != 2 {
		t.Fatalf("deleted %v, want only author 2 — a single book must protect a row", deleted)
	}
}

// Limit exists so a first apply can be run small and inspected. Sorted order makes
// the slice deterministic, so limit=1 takes the same row every time.
func TestPurgeEmptyAuthors_LimitCapsTheDeletion(t *testing.T) {
	var deleted []int
	runPurge(t, `{"apply":true,"limit":1}`, &deleted)
	if len(deleted) != 1 {
		t.Fatalf("limit=1 deleted %v, want exactly one", deleted)
	}
	if deleted[0] != 2 {
		t.Errorf("limit=1 deleted author %d, want the lowest eligible id (2) — order must be deterministic", deleted[0])
	}
}

// 🔴 A MISSING SIGNAL IS NOT PERMISSION. If the file counts cannot be read, the
// guard cannot be evaluated, and treating that as "zero files" would delete exactly
// the rows the guard protects. The op must fail instead.
func TestPurgeEmptyAuthors_FileCountFailureAbortsRatherThanDeleting(t *testing.T) {
	authors, books, _ := purgeFixture()
	var deleted []int
	store := &database.MockStore{
		GetAllAuthorsFunc:          func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc: func() (map[int]int, error) { return books, nil },
		GetAllAuthorFileCountsFunc: func() (map[int]int, error) { return nil, context.DeadlineExceeded },
		DeleteAuthorFunc: func(id int) error {
			deleted = append(deleted, id)
			return nil
		},
	}
	p := &Plugin{deps: &fakeDeps{store: store}}
	err := p.runPurgeEmptyAuthors(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{})
	if err == nil {
		t.Fatal("file-count failure did not abort the op")
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted %v despite being unable to evaluate the guard", deleted)
	}
}
