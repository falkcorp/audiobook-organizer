// file: internal/server/batch_apply_gate_test.go
// version: 1.0.0
// guid: 8b4f2d70-1e9a-4c63-a7d5-f0c3e6b91a24
// last-edited: 2026-09-13
//
// The certainty gate on both bulk-apply paths, and the dry run's read-only
// contract.

package server

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func candidateJSON(t *testing.T, c metafetch.MetadataCandidate) []json.RawMessage {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return []json.RawMessage{b}
}

// TestApplyCachedCandidate_GateRefuses pins that the cached bulk apply writes
// NOTHING for a refused candidate: no apply, no cache invalidation, no file
// work. The first row is the owner's reported failure.
func TestApplyCachedCandidate_GateRefuses(t *testing.T) {
	bigCats1 := fakeBooks{"b1": {ID: "b1", Title: "Big Cats 1", FilePath: "/lib/A/Big Cats/Big Cats 1.m4b"}}
	cases := []struct {
		name     string
		cand     metafetch.MetadataCandidate
		identity error
		want     string
	}{
		{"0.99 candidate for the wrong volume", metafetch.MetadataCandidate{Title: "Big Cats 3", SeriesPosition: "3", Score: 0.99}, nil, applygate.ReasonSequenceMismatch},
		{"0.99 candidate with no volume number", metafetch.MetadataCandidate{Title: "Big Cats", Score: 0.99}, nil, applygate.ReasonSequenceMissingOnCandidate},
		{"right volume below the floor", metafetch.MetadataCandidate{Title: "Big Cats 1", SeriesPosition: "1", Score: 0.89}, nil, applygate.ReasonScoreBelowFloor},
		{"right volume, stale cache identity", metafetch.MetadataCandidate{Title: "Big Cats 1", SeriesPosition: "1", Score: 0.99}, metafetch.ErrStaleMetadataCache, applygate.ReasonIdentityStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeApplySvc{candidates: candidateJSON(t, tc.cand), identityErr: tc.identity}
			out := applyCachedCandidateForBook(svc, bigCats1, &fakeITunes{}, "b1", true, nil)
			if out.Applied || out.Reason != applySkipGateBlocked {
				t.Fatalf("applied=%v reason=%q, want refused with %q", out.Applied, out.Reason, applySkipGateBlocked)
			}
			if out.Gate == nil || out.Gate.Reason != tc.want {
				t.Fatalf("gate = %+v, want reason %q", out.Gate, tc.want)
			}
			if len(svc.appliedIDs)+len(svc.invalidatedID)+len(svc.finishCalls) != 0 {
				t.Fatalf("refused book was touched: applied=%v invalidated=%v finish=%v",
					svc.appliedIDs, svc.invalidatedID, svc.finishCalls)
			}
		})
	}

	// And the matching volume at 0.95 goes through.
	svc := &fakeApplySvc{candidates: candidateJSON(t, metafetch.MetadataCandidate{Title: "Big Cats 1", SeriesPosition: "1", Score: 0.95})}
	if out := applyCachedCandidateForBook(svc, bigCats1, &fakeITunes{}, "b1", false, nil); !out.Applied {
		t.Fatalf("matching volume refused: reason=%q err=%v gate=%+v", out.Reason, out.Err, out.Gate)
	}
}

// TestPlanOpResultApply_GateRefuses covers /metadata/batch-apply-candidates,
// whose "matched" status had no floor behind it.
func TestPlanOpResultApply_GateRefuses(t *testing.T) {
	books := fakeBooks{"b1": {ID: "b1", Title: "Big Cats 1", Author: &database.Author{Name: "Ann Author"}}}
	cr := func(c metafetch.MetadataCandidate, fetchedTitle string) CandidateResult {
		out := CandidateResult{Status: "matched", Candidate: &c}
		out.Book.Title, out.Book.Author = fetchedTitle, "Ann Author"
		return out
	}
	if p := planOpResultApply(books, "b1", cr(metafetch.MetadataCandidate{Title: "Big Cats 3", SeriesPosition: "3", Score: 0.99}, "Big Cats 1")); p.Reason != applySkipGateBlocked || p.Gate.Reason != applygate.ReasonSequenceMismatch {
		t.Fatalf("wrong volume: reason=%q gate=%+v", p.Reason, p.Gate)
	}
	if p := planOpResultApply(books, "b1", cr(metafetch.MetadataCandidate{Title: "Big Cats 1", SeriesPosition: "1", Score: 0.99}, "Big Cats One Old Title")); p.Reason != applySkipGateBlocked || p.Gate.Reason != applygate.ReasonIdentityStale {
		t.Fatalf("title changed since fetch: reason=%q gate=%+v", p.Reason, p.Gate)
	}
	if p := planOpResultApply(books, "b1", cr(metafetch.MetadataCandidate{Title: "Big Cats 1", SeriesPosition: "1", Score: 0.95}, "Big Cats 1")); p.Reason != "" {
		t.Fatalf("matching volume refused: reason=%q err=%v", p.Reason, p.Err)
	}
}

// writeMethodPrefixes names every MockStore method that mutates state.
var writeMethodPrefixes = []string{
	"Create", "Update", "Delete", "Set", "Put", "Upsert", "Add", "Remove", "Save",
	"Insert", "Mark", "Record", "Increment", "Apply", "Write", "Clear", "Merge",
	"Move", "Replace", "Link", "Unlink", "Reset", "Purge", "Bump", "Touch",
	"Rename", "Import", "Enqueue", "Append", "Prune", "Restore", "Revert",
}

// failOnWriteStore returns a MockStore whose every write method fails the
// test. Built by reflection over the whole struct, so a write method added to
// the store later is covered without editing this list of names — only a new
// VERB would need adding to writeMethodPrefixes.
//
// Nothing is exempt. The preview op's own report rows (CreateOperationResult)
// go to a different store in runBulkApplyPreview; the per-book path tested
// here must not write at all.
func failOnWriteStore(t *testing.T) *database.MockStore {
	t.Helper()
	m := &database.MockStore{}
	v := reflect.ValueOf(m).Elem()
	typ := v.Type()
	armed := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.Func || !strings.HasSuffix(f.Name, "Func") {
			continue
		}
		method := strings.TrimSuffix(f.Name, "Func")
		isWrite := false
		for _, p := range writeMethodPrefixes {
			if strings.HasPrefix(method, p) {
				isWrite = true
				break
			}
		}
		if !isWrite {
			continue
		}
		ft := f.Type
		v.Field(i).Set(reflect.MakeFunc(ft, func([]reflect.Value) []reflect.Value {
			t.Errorf("dry run called store write %s", method)
			outs := make([]reflect.Value, ft.NumOut())
			for j := range outs {
				outs[j] = reflect.Zero(ft.Out(j))
			}
			return outs
		}))
		armed++
	}
	if armed < 50 {
		t.Fatalf("only %d write methods armed; the prefix list no longer matches MockStore", armed)
	}
	return m
}

// TestBulkApplyPreview_WritesNothing drives the dry run's per-book path —
// the plan the real apply acts on, then the field and rename preview — against
// a real *metafetch.Service over a store that fails the test on any write,
// with auto-rename on so the rename planner runs too.
func TestBulkApplyPreview_WritesNothing(t *testing.T) {
	oldRename, oldRoot := config.AppConfig.AutoRenameOnApply, config.AppConfig.RootDir
	config.AppConfig.AutoRenameOnApply, config.AppConfig.RootDir = true, ""
	t.Cleanup(func() { config.AppConfig.AutoRenameOnApply, config.AppConfig.RootDir = oldRename, oldRoot })

	store := failOnWriteStore(t)
	authorID := 7
	books := map[string]*database.Book{
		"good":  {ID: "good", Title: "Big Cats 1", AuthorID: &authorID, FilePath: "/lib/Ann Author/Big Cats/Big Cats 1.m4b"},
		"wrong": {ID: "wrong", Title: "Big Cats 1", AuthorID: &authorID, FilePath: "/lib/Ann Author/Big Cats/Big Cats 1.m4b"},
	}
	cands := map[string]metafetch.MetadataCandidate{
		"good":  {Title: "Big Cats 1", Author: "Ann Author", Series: "Big Cats", SeriesPosition: "1", Score: 0.95, ASIN: "B000TEST01", Source: "Audible"},
		"wrong": {Title: "Big Cats 3", Author: "Ann Author", Series: "Big Cats", SeriesPosition: "3", Score: 0.99, ASIN: "B000TEST03", Source: "Audible"},
	}
	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if b, ok := books[id]; ok {
			cp := *b
			return &cp, nil
		}
		return nil, nil
	}
	store.GetAuthorByIDFunc = func(int) (*database.Author, error) { return &database.Author{ID: authorID, Name: "Ann Author"}, nil }
	store.GetMetadataCacheFunc = func(id string) (*database.MetadataCandidateCache, error) {
		c, ok := cands[id]
		if !ok {
			return nil, nil
		}
		// Empty SourceHash: the legacy fail-open row, so identity passes and
		// the preview reaches the field and rename planners.
		return &database.MetadataCandidateCache{BookID: id, Candidates: candidateJSON(t, c)}, nil
	}
	store.GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
		return []database.BookFile{{ID: "f-" + id, BookID: id, FilePath: books[strings.TrimSuffix(id, "")].FilePath, Format: "m4b"}}, nil
	}

	svc := metafetch.NewService(store)
	rows := map[string]bulkApplyPreviewRow{}
	for id := range books {
		rows[id] = previewBulkApplyRow(svc, id, planCachedApply(svc, store, id), true)
	}

	good := rows["good"]
	if good.Verdict != previewVerdictApply {
		t.Fatalf("good: verdict %q reason %q detail %q", good.Verdict, good.Reason, good.Detail)
	}
	if !hasChange(good.Changes, "asin", "B000TEST01") {
		t.Errorf("good: changes %+v do not report the ASIN the apply would write", good.Changes)
	}
	if good.Rename == nil {
		t.Errorf("good: no rename preview")
	}
	wrong := rows["wrong"]
	if wrong.Verdict != previewVerdictBlocked || wrong.Reason != applygate.ReasonSequenceMismatch {
		t.Fatalf("wrong: verdict %q reason %q, want blocked %q", wrong.Verdict, wrong.Reason, applygate.ReasonSequenceMismatch)
	}
	if !hasChange(wrong.Changes, "title", "Big Cats 3") {
		t.Errorf("wrong: blocked row should still show what it would have written; changes %+v", wrong.Changes)
	}
}

func hasChange(changes []metafetch.FieldChange, field, newV string) bool {
	for _, c := range changes {
		if c.Field == field && c.New == newV {
			return true
		}
	}
	return false
}

// A preview error must never be reported as "apply".
func TestPreviewBulkApplyRow_PreviewErrorIsNotApply(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Dune", Score: 0.95}
	v := applygate.Evaluate(&database.Book{Title: "Dune"}, &cand, nil)
	plan := cachedApplyPlan{Book: &database.Book{ID: "b", Title: "Dune"}, Candidate: &cand, Gate: &v}
	row := previewBulkApplyRow(erroringPreview{&fakeApplySvc{}}, "b", plan, true)
	if row.Verdict != previewVerdictBlocked || row.Reason != "preview_failed" {
		t.Fatalf("verdict %q reason %q", row.Verdict, row.Reason)
	}
}

type erroringPreview struct{ *fakeApplySvc }

func (erroringPreview) PreviewMetadataCandidate(string, metafetch.MetadataCandidate, bool) (*metafetch.ApplyPreview, error) {
	return nil, errors.New("locks unavailable")
}
