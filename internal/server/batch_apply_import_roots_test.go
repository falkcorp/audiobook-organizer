// file: internal/server/batch_apply_import_roots_test.go
// version: 1.0.0
// guid: 9e4b27c1-5a3d-4f80-b6c2-0d7e18a9f354
// last-edited: 2026-10-04

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// importRootBooks is fakeBooks whose import-path read is scripted and whose
// folder listing answers from rows (prefix match, like the store).
type importRootBooks struct {
	fakeBooks
	rows     map[string]string
	files    map[string][]database.BookFile
	rootsErr error
	roots    []string
	reads    *atomic.Int32
}

func (b importRootBooks) GetAllImportPaths() ([]database.ImportPath, error) {
	if b.reads != nil {
		b.reads.Add(1)
	}
	if b.rootsErr != nil {
		return nil, b.rootsErr
	}
	out := make([]database.ImportPath, 0, len(b.roots))
	for _, r := range b.roots {
		out = append(out, database.ImportPath{Path: r})
	}
	return out, nil
}

func (b importRootBooks) LiveBookPathsUnderDir(dir string) (map[string]string, error) {
	out := map[string]string{}
	for id, p := range b.rows {
		if strings.HasPrefix(p, dir+"/") {
			out[id] = p
		}
	}
	return out, nil
}

func (b importRootBooks) GetBookFiles(id string) ([]database.BookFile, error) {
	return b.files[id], nil
}

// A blank-titled Big Finish file directly in an import root, whose intro
// transcription is the only Doctor Who signal, is still refused by the bulk
// owner-manual rule when the import-root read fails. The resolver then lists
// the root, sees the other files as siblings and SKIPS the row with no query,
// so the guard can no longer match the transcription through the search
// query; it now checks the transcribed stand-ins itself.
func TestBulkApply_ManualOnlyGuardSurvivesAMissedImportRoot(t *testing.T) {
	const (
		root   = "/imports/Rips"
		chimes = "Doctor Who: The Chimes of Midnight"
	)
	dur := 1800
	path := root + "/01.mp3"
	book := &database.Book{ID: "b1", Title: "", TranscribedTitle: strPtr(chimes), FilePath: path, Duration: &dur}
	rows := map[string]string{"b1": path, "b2": root + "/02.mp3", "b3": root + "/03.mp3", "b4": root + "/04.mp3"}
	files := map[string][]database.BookFile{"b1": {{FilePath: path, Duration: dur, FileSize: int64(dur) * 8000, TranscribedTitle: strPtr(chimes)}}}
	// Audible answered with a record that names nothing manual-only.
	cand := metafetch.MetadataCandidate{Title: "The Chimes of Midnight", Author: "Robert Shearman", Score: 0.95, DurationSec: dur, Source: "Audible"}
	blob, err := json.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	stale := fmt.Errorf("%w: book b1 (stored a, current b)", metafetch.ErrStaleMetadataCache)
	books := importRootBooks{fakeBooks: fakeBooks{"b1": book}, rows: rows, files: files, rootsErr: errors.New("store down")}

	if q := metabatch.ResolveCandidateSearchQuery(books, book); q.Usable || q.Title != "" {
		t.Fatalf("fixture: resolver returned %+v; the test needs the missed-root skip (no query)", q)
	}
	// Without the rule the gate would refuse identity_stale, which a review
	// page bulk button's pin overrides: the bulk-pin case is the one that
	// would have applied a Doctor Who book.
	bulkPin := metafetch.PinOf(cand)
	bulkPin.Origin = metafetch.PinOriginReviewBulk
	for _, pin := range []*metafetch.CandidatePin{nil, &bulkPin} {
		svc := &fakeApplySvc{candidates: []json.RawMessage{blob}, identityErr: stale}
		plan := planCachedApply(svc, books, "b1", nil, pin)
		if plan.Gate == nil {
			t.Fatalf("pin=%v: gate did not run: %+v", pin != nil, plan)
		}
		if plan.Gate.Allowed || plan.OwnerReviewed || plan.Gate.Reason != applygate.ReasonOwnerManualOnly {
			t.Fatalf("pin=%v: gate = %q (%s), owner-reviewed %v; want a hard %q", pin != nil,
				plan.Gate.Reason, plan.Gate.Detail, plan.OwnerReviewed, applygate.ReasonOwnerManualOnly)
		}
		if !strings.Contains(plan.Gate.Detail, "transcribed title") {
			t.Errorf("pin=%v: detail %q does not name the transcribed stand-in it matched", pin != nil, plan.Gate.Detail)
		}
	}
}

// One apply call reads the import paths once, however many books it plans
// and however many resolver calls each book makes (withCachedImportPaths).
func TestWithCachedImportPaths_OneReadPerApplyCall(t *testing.T) {
	var reads atomic.Int32
	dur := 1800
	fb := fakeBooks{}
	rows := map[string]string{}
	files := map[string][]database.BookFile{}
	for i := range 20 {
		id := fmt.Sprintf("b%d", i)
		p := fmt.Sprintf("/library/Author %d/Book %d/Book %d.m4b", i, i, i)
		fb[id] = &database.Book{ID: id, Title: "", FilePath: p, Duration: &dur}
		rows[id] = p
	}
	books := withCachedImportPaths(importRootBooks{fakeBooks: fb, rows: rows, files: files, roots: []string{"/imports/x"}, reads: &reads})
	for _, b := range fb {
		for range 3 { // the guard, the transcribed-search check, the op-result check
			metabatch.ResolveCandidateSearchQuery(books, b)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("import paths read %d times across 20 books x 3 resolves, want 1", n)
	}
	if again, ok := withCachedImportPaths(books).(*importRootsCachedBooks); !ok || again != books.(*importRootsCachedBooks) {
		t.Error("wrapping an already-wrapped reader added a second cache")
	}
}
