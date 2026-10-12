// file: internal/server/handlers/dedup/itunes_flags_test.go
// version: 1.1.0
// guid: 3e9a6d12-8c47-4f05-b1d3-7a20c5e84f96
// last-edited: 2026-10-11

package deduphandler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunesguard"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
)

// TestListDedupCandidates_IncludeBooks_ITunesFlag pins book_a.is_itunes /
// book_b.is_itunes: one entry per signal itunesguard.OwnershipWhy counts, a
// plain book left false, and the cost shape (one batched file read for the
// page; the external-id read only for a book nothing else settled).
func TestListDedupCandidates_IncludeBooks_ITunesFlag(t *testing.T) {
	h, d := newHandler(t)
	pid := "0123456789ABCDEF"

	// Pair ordering puts the smaller id on side A, so the ids are chosen to
	// make a-* the A side of every pair.
	insertCandidate(t, d.es, "a-pid", "b-plain")
	insertCandidate(t, d.es, "a-rowpid", "b-extid")
	insertCandidate(t, d.es, "a-media", "b-rowpath")

	books := map[string]*database.Book{
		"a-pid":     {ID: "a-pid", FilePath: "/lib/a-pid.m4b", ITunesPersistentID: &pid},
		"b-plain":   {ID: "b-plain", FilePath: "/lib/b-plain.m4b"},
		"a-rowpid":  {ID: "a-rowpid", FilePath: "/lib/a-rowpid.m4b"},
		"b-extid":   {ID: "b-extid", FilePath: "/lib/b-extid.m4b"},
		"a-media":   {ID: "a-media", FilePath: "/Users/x/Music/iTunes/iTunes Media/Audiobooks/a.m4b"},
		"b-rowpath": {ID: "b-rowpath", FilePath: "/lib/b-rowpath.m4b"},
	}
	for id, b := range books {
		d.store.EXPECT().GetBookByID(id).Return(b, nil).Maybe()
	}
	d.store.EXPECT().GetBookFilesForIDsCore(mock.Anything).RunAndReturn(
		func(ids []string) (map[string][]database.BookFileCore, error) {
			if len(ids) != len(books) {
				t.Errorf("file read named %d books, want the page's %d distinct books once: %v", len(ids), len(books), ids)
			}
			return map[string][]database.BookFileCore{
				"a-rowpid":  {{ID: "f1", BookID: "a-rowpid", FilePath: "/lib/a-rowpid.m4b", ITunesPersistentID: "FEDCBA9876543210"}},
				"b-rowpath": {{ID: "f2", BookID: "b-rowpath", FilePath: "/lib/b-rowpath.m4b", ITunesPath: "/Users/x/Music/iTunes/b.m4b"}},
			}, nil
		}).Once()
	// Only books no cheaper signal settled reach the external-id read: a-pid,
	// a-rowpid, a-media and b-rowpath are decided before it.
	for _, id := range []string{"b-plain", "b-extid"} {
		var exts []database.ExternalIDMapping
		if id == "b-extid" {
			exts = []database.ExternalIDMapping{{Source: "itunes", ExternalID: "ABCDEF0123456789", BookID: id}}
		}
		d.store.EXPECT().GetExternalIDsForBook(id).Return(exts, nil).Once()
	}

	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?include_books=true", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Candidates []map[string]any `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	got := map[string]bool{}
	for _, row := range resp.Data.Candidates {
		for _, side := range []string{"book_a", "book_b"} {
			b, ok := row[side].(map[string]any)
			if !ok {
				t.Fatalf("%s missing: %v", side, row)
			}
			flag, ok := b["is_itunes"].(bool)
			if !ok {
				t.Fatalf("%s.is_itunes is not a boolean: %v", side, b)
			}
			// The stored book's own fields are still there next to the flag.
			if b["id"] == nil || b["file_path"] == nil {
				t.Fatalf("%s lost its book fields: %v", side, b)
			}
			got[b["id"].(string)] = flag
		}
	}
	want := map[string]bool{
		"a-pid": true, "b-plain": false,
		"a-rowpid": true, "b-extid": true,
		"a-media": true, "b-rowpath": true,
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("is_itunes[%s]=%v want %v", id, got[id], w)
		}
	}
}

// A failed read must fail the list. Dropping the flag to false would hide the
// chip on exactly the book the reader needs warned about.
func TestListDedupCandidates_IncludeBooks_ITunesFlagReadErrorFailsList(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "a-1", "b-1")
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x", FilePath: "/lib/x.m4b"}, nil).Maybe()
	d.store.EXPECT().GetBookFilesForIDsCore(mock.Anything).Return(nil, errors.New("boom")).Once()

	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?include_books=true", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; body=%s", w.Code, w.Body.String())
	}
}

// listIsITunes drives the include_books listing and returns is_itunes per book
// id; a book whose object has no is_itunes key is absent from the map.
func listIsITunes(t *testing.T, h *deduphandler.Handler) map[string]bool {
	t.Helper()
	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?include_books=true", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Candidates []map[string]any `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	got := map[string]bool{}
	for _, row := range resp.Data.Candidates {
		for _, side := range []string{"book_a", "book_b"} {
			b, ok := row[side].(map[string]any)
			if !ok {
				t.Fatalf("%s missing: %v", side, row)
			}
			if v, has := b["is_itunes"]; has {
				flag, isBool := v.(bool)
				if !isBool {
					t.Fatalf("%s.is_itunes is not a boolean: %v", side, v)
				}
				got[b["id"].(string)] = flag
			}
		}
	}
	return got
}

// A failed external-id read fails the list (500), like a failed file read. A
// swallowed one would drop the chip from exactly the book whose only iTunes
// signal is that external id.
func TestListDedupCandidates_IncludeBooks_ITunesFlagExternalIDReadErrorFailsList(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "a-1", "b-1")
	d.store.EXPECT().GetBookByID(mock.Anything).RunAndReturn(func(id string) (*database.Book, error) {
		return &database.Book{ID: id, FilePath: "/lib/" + id + ".m4b"}, nil
	}).Maybe()
	d.store.EXPECT().GetBookFilesForIDsCore(mock.Anything).Return(map[string][]database.BookFileCore{}, nil).Once()
	d.store.EXPECT().GetExternalIDsForBook(mock.Anything).Return(nil, errors.New("ext boom")).Maybe()

	w := doReq(t, h.ListDedupCandidates, http.MethodGet, "/api/v1/dedup/candidates?include_books=true", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; body=%s", w.Code, w.Body.String())
	}
}

// One book on two pairs is read once: the file read names it once and its
// external ids are fetched once.
func TestListDedupCandidates_IncludeBooks_ITunesFlagBookOnTwoPairsReadOnce(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "a-1", "b-1")
	insertCandidate(t, d.es, "a-1", "b-2")
	d.store.EXPECT().GetBookByID(mock.Anything).RunAndReturn(func(id string) (*database.Book, error) {
		return &database.Book{ID: id, FilePath: "/lib/" + id + ".m4b"}, nil
	}).Maybe()
	d.store.EXPECT().GetBookFilesForIDsCore(mock.Anything).RunAndReturn(
		func(ids []string) (map[string][]database.BookFileCore, error) {
			seen := map[string]int{}
			for _, id := range ids {
				seen[id]++
			}
			if len(ids) != 3 || seen["a-1"] != 1 {
				t.Errorf("file read named %v, want a-1, b-1, b-2 once each", ids)
			}
			return nil, nil
		}).Once()
	for _, id := range []string{"a-1", "b-1", "b-2"} {
		d.store.EXPECT().GetExternalIDsForBook(id).Return(nil, nil).Once()
	}
	got := listIsITunes(t, h)
	for _, id := range []string{"a-1", "b-1", "b-2"} {
		if v, ok := got[id]; !ok || v {
			t.Errorf("is_itunes[%s]=%v present=%v want false present", id, v, ok)
		}
	}
}

// Signals the main test does not cover: a book_file row whose own path is in
// an iTunes folder while the book's path is clean, a path under books/itunes/**,
// and a tombstoned / non-iTunes external id (not an iTunes copy).
func TestListDedupCandidates_IncludeBooks_ITunesFlagPathsAndExternalIDs(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "a-rowfile", "b-tree")
	insertCandidate(t, d.es, "a-tomb", "b-other")

	books := map[string]*database.Book{
		"a-rowfile": {ID: "a-rowfile", FilePath: "/lib/clean.m4b"},
		"b-tree":    {ID: "b-tree", FilePath: "/lib/books/itunes/Author/b.m4b"},
		"a-tomb":    {ID: "a-tomb", FilePath: "/lib/a-tomb.m4b"},
		"b-other":   {ID: "b-other", FilePath: "/lib/b-other.m4b"},
	}
	for id, b := range books {
		d.store.EXPECT().GetBookByID(id).Return(b, nil).Maybe()
	}
	d.store.EXPECT().GetBookFilesForIDsCore(mock.Anything).Return(map[string][]database.BookFileCore{
		"a-rowfile": {{ID: "f1", BookID: "a-rowfile", FilePath: "/Users/x/Music/iTunes/iTunes Media/Audiobooks/r.m4b"}},
	}, nil).Once()
	d.store.EXPECT().GetExternalIDsForBook("a-tomb").Return([]database.ExternalIDMapping{
		{Source: "itunes", ExternalID: "AAAA", BookID: "a-tomb", Tombstoned: true},
	}, nil).Once()
	d.store.EXPECT().GetExternalIDsForBook("b-other").Return([]database.ExternalIDMapping{
		{Source: "audible", ExternalID: "B000", BookID: "b-other"},
	}, nil).Once()

	got := listIsITunes(t, h)
	want := map[string]bool{"a-rowfile": true, "b-tree": true, "a-tomb": false, "b-other": false}
	for id, w := range want {
		if v, ok := got[id]; !ok || v != w {
			t.Errorf("is_itunes[%s]=%v present=%v want %v", id, v, ok, w)
		}
	}
}

// A path whose location cannot be settled (an unreadable folder) is doubt, and
// doubt is not proof: the chip is not shown.
func TestListDedupCandidates_IncludeBooks_ITunesFlagDoubtIsNotITunes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-000 folder, so there is no doubt to provoke")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	// Confirm the fixture really is unresolvable, so the test cannot pass for
	// the wrong reason.
	res := repairs.NewPathResolver()
	p := filepath.Join(locked, "sub", "book.m4b")
	if _, doubt := itunesguard.OwnershipWhy(res, "x", "", []string{p}, nil, nil, true); !doubt {
		t.Skip("this filesystem does not give doubt for a mode-000 folder")
	}

	h, d := newHandler(t)
	insertCandidate(t, d.es, "a-1", "b-1")
	d.store.EXPECT().GetBookByID(mock.Anything).RunAndReturn(func(id string) (*database.Book, error) {
		return &database.Book{ID: id, FilePath: p}, nil
	}).Maybe()
	d.store.EXPECT().GetBookFilesForIDsCore(mock.Anything).Return(nil, nil).Once()
	d.store.EXPECT().GetExternalIDsForBook(mock.Anything).Return(nil, nil).Maybe()
	got := listIsITunes(t, h)
	for _, id := range []string{"a-1", "b-1"} {
		if v, ok := got[id]; !ok || v {
			t.Errorf("is_itunes[%s]=%v present=%v want false present", id, v, ok)
		}
	}
}

// A hung mount must not hang the list: past the budget the books the disk pass
// did not reach carry no is_itunes key (the client falls back to its own
// check), the ones it did settle keep theirs, and the request still answers 200.
func TestListDedupCandidates_IncludeBooks_ITunesFlagBudgetLeavesUnsettledAbsent(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	defer deduphandler.SetITunesFlagBudgetForTest(100 * time.Millisecond)()
	defer deduphandler.SetOwnershipWhyForTest(func(_ *repairs.PathResolver, id, _ string, _ []string, _ []itunesguard.Row, _ []database.ExternalIDMapping, _ bool) (string, bool) {
		if id == "b-hung" {
			<-release
		}
		// A non-empty answer settles the book without touching the store, so
		// the abandoned worker does nothing after the test ends.
		return "fake", false
	})()

	h, d := newHandler(t)
	insertCandidate(t, d.es, "a-fast", "b-hung")
	d.store.EXPECT().GetBookByID(mock.Anything).RunAndReturn(func(id string) (*database.Book, error) {
		return &database.Book{ID: id, FilePath: "/lib/" + id + ".m4b"}, nil
	}).Maybe()
	d.store.EXPECT().GetBookFilesForIDsCore(mock.Anything).Return(nil, nil).Once()

	start := time.Now()
	got := listIsITunes(t, h)
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("list took %s; the budget did not bound it", el)
	}
	if v, ok := got["a-fast"]; !ok || !v {
		t.Errorf("a-fast: is_itunes=%v present=%v want true present", v, ok)
	}
	if _, ok := got["b-hung"]; ok {
		t.Errorf("b-hung carries is_itunes, want it absent (unsettled)")
	}
}
