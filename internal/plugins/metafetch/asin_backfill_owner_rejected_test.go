// file: internal/plugins/metafetch/asin_backfill_owner_rejected_test.go
// version: 1.0.0
// guid: 8a3e5c17-2f4d-4b90-9e61-7c0d2b4f8a35
// last-edited: 2026-10-10

package metafetch

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The ASIN backfill must never write the ASIN (or ISBN) of an Audible
// record the owner rejected for the book (review round 3, S1). All
// fixtures are synthetic.

const (
	synBackfillISBN  = "9780306406157"
	synBackfillASIN  = "B0SYNBACK1"
	synBackfillTitle = "Synthetic Field Notes 7"
)

func synBackfillProduct() metadata.AudibleIdentity {
	return metadata.AudibleIdentity{ASIN: synBackfillASIN, Title: synBackfillTitle, Authors: []string{"Synthetic Penwright"},
		ISBN: synBackfillISBN, RuntimeMin: 600}
}

func synBackfillFake() *fakeAudible {
	p := synBackfillProduct()
	return &fakeAudible{
		byTitle: map[string][]metadata.AudibleIdentity{"synthetic field notes 7": {p}},
		byKW:    map[string][]metadata.AudibleIdentity{synBackfillISBN: {p}},
		byASIN:  map[string]*metadata.AudibleIdentity{synBackfillASIN: &p},
	}
}

func rejectRecord(t *testing.T, s *database.PebbleStore, bookID, source string) {
	t.Helper()
	if err := metafetch.RecordRejection(s, bookID, &metafetch.MetadataCandidate{Source: source, Title: synBackfillTitle, ASIN: synBackfillASIN}); err != nil {
		t.Fatal(err)
	}
}

func TestASINBackfill_NeverWritesAnOwnerRejectedASIN(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"rejected under Audible", "Audible"},
		{"rejected under Audnexus, same ASIN", "Audnexus (Audible)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fa := synBackfillFake()
			p, s := newASINTestPlugin(t, fa)
			aid := mkAuthor(t, s, "Synthetic Penwright")
			b := mkBook(t, s, &database.Book{Title: synBackfillTitle, AuthorID: &aid, ISBN13: new(synBackfillISBN), FilePath: "/x/sfn7.m4b"})

			// Fixture: without the rejection the dry run would write it.
			if res := runASIN(t, p, `{}`).res(t); res.MatchedWritten != 1 {
				t.Fatalf("fixture: dry run without a rejection = %+v, want one match", res)
			}
			rejectRecord(t, s, b.ID, tc.source)

			dry := runASIN(t, p, `{}`).res(t)
			if dry.MatchedWritten != 0 || dry.RejectReasons[asinRejectOwnerRejected] != 1 {
				t.Fatalf("dry run = %+v, want the match refused as owner_rejected", dry)
			}
			live := runASIN(t, p, `{"dry_run":false}`).res(t)
			if got := asinOf(t, s, b.ID); got != "" {
				t.Fatalf("wrote the owner-rejected ASIN %q", got)
			}
			if live.MatchedWritten != 0 || live.RejectReasons[asinRejectOwnerRejected] != 1 {
				t.Fatalf("live run = %+v, want the match refused as owner_rejected", live)
			}
		})
	}
}

func TestASINBackfill_NeverWritesTheISBNOfAnOwnerRejectedASIN(t *testing.T) {
	fa := synBackfillFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Synthetic Penwright")
	b := mkBook(t, s, &database.Book{Title: synBackfillTitle, AuthorID: &aid, ASIN: new(synBackfillASIN), FilePath: "/x/sfn7.m4b"})
	if res := runASIN(t, p, `{}`).res(t); res.ISBNWritten != 1 {
		t.Fatalf("fixture: dry run without a rejection = %+v, want one ISBN", res)
	}
	rejectRecord(t, s, b.ID, "Audible")

	if dry := runASIN(t, p, `{}`).res(t); dry.ISBNWritten != 0 || dry.RejectReasons[asinRejectOwnerRejected] != 1 {
		t.Fatalf("dry run = %+v", dry)
	}
	live := runASIN(t, p, `{"dry_run":false}`).res(t)
	if i13, _ := isbnOf(t, s, b.ID); i13 != "" {
		t.Fatalf("wrote the owner-rejected record's ISBN %q", i13)
	}
	if live.ISBNWritten != 0 || live.RejectReasons[asinRejectOwnerRejected] != 1 {
		t.Fatalf("live run = %+v", live)
	}
}

// failingRejectionsStore cannot read the owner's rejections.
type failingRejectionsStore struct{ *database.PebbleStore }

func (f failingRejectionsStore) ScanPrefix(prefix string) ([]database.KVPair, error) {
	if strings.HasPrefix(prefix, metafetch.RejectedCandidatePrefix) {
		return nil, errors.New("scan failed")
	}
	return f.PebbleStore.ScanPrefix(prefix)
}

func TestASINBackfill_UnreadableRejectionsWriteNothing(t *testing.T) {
	fa := synBackfillFake()
	_, s := newASINTestPlugin(t, fa)
	p := New(failingRejectionsStore{s}, nil)
	p.newAudible = func() audibleIdentitySearcher { return fa }
	aid := mkAuthor(t, s, "Synthetic Penwright")
	b := mkBook(t, s, &database.Book{Title: synBackfillTitle, AuthorID: &aid, ISBN13: new(synBackfillISBN), FilePath: "/x/sfn7.m4b"})

	res := runASIN(t, p, `{"dry_run":false}`).res(t)
	if got := asinOf(t, s, b.ID); got != "" {
		t.Fatalf("wrote ASIN %q with the rejections unreadable", got)
	}
	if res.MatchedWritten != 0 || res.Errored != 1 {
		t.Fatalf("result = %+v, want one errored book and nothing written", res)
	}
}
