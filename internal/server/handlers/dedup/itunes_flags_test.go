// file: internal/server/handlers/dedup/itunes_flags_test.go
// version: 1.0.0
// guid: 3e9a6d12-8c47-4f05-b1d3-7a20c5e84f96
// last-edited: 2026-10-11

package deduphandler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
