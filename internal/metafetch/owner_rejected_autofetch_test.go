// file: internal/metafetch/owner_rejected_autofetch_test.go
// version: 1.2.3
// guid: 7c2e4a90-1b3d-4f6e-8a25-9d0c1e7f3b48
// last-edited: 2026-10-15

package metafetch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The auto-fetch (FetchMetadataForBook, FetchMetadataForBookByTitle)
// applies a fresh search's best record with nobody picking it: the iTunes
// import enrichment, organize's fetch-first, the fetch endpoint and its
// queued form, and the production-company resolvers. A record the owner
// rejected for the book must never be the one it applies (regression,
// 2026-10-10). All fixtures are synthetic.

const rejFetchTitle = "Synthetic Field Notes 5"

type autoFetchRun struct {
	label string
	run   func(*Service, string) (*FetchMetadataResponse, error)
}

var autoFetchRuns = []autoFetchRun{
	{"FetchMetadataForBook", func(s *Service, id string) (*FetchMetadataResponse, error) {
		return s.FetchMetadataForBook(context.Background(), id)
	}},
	{"FetchMetadataForBookByTitle", func(s *Service, id string) (*FetchMetadataResponse, error) {
		return s.FetchMetadataForBookByTitle(context.Background(), id)
	}},
}

func rejFetchRecord(asin, desc string) metadata.BookMetadata {
	return metadata.BookMetadata{Title: rejFetchTitle, Author: "Synthetic Penwright", ASIN: asin, Description: desc}
}

// rejectFor stores the owner's rejection of source's record r for bookID,
// built the way the reject handler builds it: from the candidate the
// search turned r into (its Source is the provider's Name()).
func rejectFor(t *testing.T, store RejectionStore, bookID, source string, r metadata.BookMetadata) {
	t.Helper()
	c := newSearchCandidate(r, source, 0.9, nil, localRuntime{}, false)
	require.NoError(t, RecordRejection(store, bookID, &c))
}

func rejFetchFixture(t *testing.T, sources ...metadata.MetadataSource) (*verdictFixture, string) {
	t.Helper()
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: rejFetchTitle, FilePath: "/lib/sfn/book.m4b"})
	require.NoError(t, err)
	f.mfs.SetOverrideSources(sources)
	return f, b.ID
}

// Every record the chain returns is rejected: nothing is applied, and the
// error says the owner rejected them.
func TestAutoFetch_AllRecordsOwnerRejectedAppliesNothing(t *testing.T) {
	for _, fetch := range autoFetchRuns {
		t.Run(fetch.label, func(t *testing.T) {
			rec := rejFetchRecord("B0SYNREJ01", "rejected description")
			f, id := rejFetchFixture(t, &verdictSource{name: "Audible", results: []metadata.BookMetadata{rec}})
			rejectFor(t, f.store, id, "Audible", rec)

			_, err := fetch.run(f.mfs, id)
			require.ErrorIs(t, err, ErrOwnerRejectedNotApplied)
			var ore *OwnerRejectedNotAppliedError
			require.ErrorAs(t, err, &ore)
			require.Equal(t, []string{"Audible"}, ore.Sources)
			require.Contains(t, err.Error(), "owner_rejected")
			require.Nil(t, f.book(id).Description, "an owner-rejected record was applied")
		})
	}
}

// The source's best record is rejected: the next record of the same source
// is applied instead.
func TestAutoFetch_RejectedBestFallsThroughToNextRecord(t *testing.T) {
	for _, fetch := range autoFetchRuns {
		t.Run(fetch.label, func(t *testing.T) {
			rejected := rejFetchRecord("B0SYNREJ01", "rejected description")
			kept := rejFetchRecord("B0SYNKEP02", "kept description")
			f, id := rejFetchFixture(t, &verdictSource{name: "Audible", results: []metadata.BookMetadata{rejected, kept}})
			// The fixture must put the rejected record first, or the test
			// proves nothing.
			scored := f.mfs.bestTitleMatchForBook(f.book(id), []metadata.BookMetadata{rejected, kept}, "", "", rejFetchTitle)
			require.NotEmpty(t, scored)
			require.Equal(t, "B0SYNREJ01", scored[0].ASIN, "fixture: the rejected record must rank first")
			rejectFor(t, f.store, id, "Audible", rejected)

			resp, err := fetch.run(f.mfs, id)
			require.NoError(t, err)
			require.Equal(t, "Audible", resp.Source)
			got := f.book(id)
			require.NotNil(t, got.Description)
			require.Equal(t, "kept description", *got.Description, "the rejected record was applied")
		})
	}
}

// Source A's only record is rejected: source B's record is applied.
func TestAutoFetch_RejectedSourceFallsThroughToNextSource(t *testing.T) {
	for _, fetch := range autoFetchRuns {
		t.Run(fetch.label, func(t *testing.T) {
			recA := rejFetchRecord("B0SYNREJ01", "source A description")
			recB := rejFetchRecord("B0SYNKEP02", "source B description")
			a := &verdictSource{name: "Audible", results: []metadata.BookMetadata{recA}}
			b := &verdictSource{name: "Synthetic Source B", results: []metadata.BookMetadata{recB}}
			f, id := rejFetchFixture(t, a, b)
			rejectFor(t, f.store, id, "Audible", recA)

			resp, err := fetch.run(f.mfs, id)
			require.NoError(t, err)
			require.Equal(t, "Synthetic Source B", resp.Source)
			got := f.book(id)
			require.NotNil(t, got.Description)
			require.Equal(t, "source B description", *got.Description)
		})
	}
}

// failingRejectionScan is a store whose rejection keyspace cannot be read.
type failingRejectionScan struct{ *database.PebbleStore }

func (s failingRejectionScan) ScanPrefix(prefix string) ([]database.KVPair, error) {
	if strings.HasPrefix(prefix, RejectedCandidatePrefix) {
		return nil, errors.New("scan failed")
	}
	return s.PebbleStore.ScanPrefix(prefix)
}

// An unreadable rejection list fails the book closed before any source is
// asked: the fetch cannot tell whether the owner rejected what it would
// apply.
func TestAutoFetch_UnreadableRejectionsFailClosed(t *testing.T) {
	for _, fetch := range autoFetchRuns {
		t.Run(fetch.label, func(t *testing.T) {
			src := &verdictSource{name: "Audible", results: []metadata.BookMetadata{rejFetchRecord("B0SYNKEP02", "description")}}
			f, id := rejFetchFixture(t, src)
			svc := NewService(failingRejectionScan{f.store})
			svc.SetOverrideSources([]metadata.MetadataSource{src})

			_, err := fetch.run(svc, id)
			require.ErrorIs(t, err, ErrOwnerRejectionCheckFailed)
			require.Zero(t, src.calls.Load(), "no source may be searched")
			require.Nil(t, f.book(id).Description)
		})
	}
}

// B1 (round 3): the default chain is Audible, then Open Library (review
// only), then Audnexus, which serves the same Audible product by the book's
// ASIN. A record rejected under Audible must not come back through
// Audnexus.
func TestAutoFetch_AudibleRejectionRefusesAudnexusSameASIN(t *testing.T) {
	for _, fetch := range autoFetchRuns {
		t.Run(fetch.label, func(t *testing.T) {
			rec := rejFetchRecord("B0SYNREJ01", "rejected description")
			audible := &verdictSource{name: "Audible", results: []metadata.BookMetadata{rec}}
			audnexus := &verdictSource{name: audnexusSource, results: []metadata.BookMetadata{rec}}
			f, id := rejFetchFixture(t, audible, audnexus)
			rejectFor(t, f.store, id, "Audible", rec)

			_, err := fetch.run(f.mfs, id)
			require.ErrorIs(t, err, ErrOwnerRejectedNotApplied)
			var ore *OwnerRejectedNotAppliedError
			require.ErrorAs(t, err, &ore)
			require.Equal(t, []string{"Audible", audnexusSource}, ore.Sources)
			require.Positive(t, audnexus.calls.Load(), "fixture: Audnexus must have been asked")
			require.Nil(t, f.book(id).Description, "the rejected record was applied through Audnexus")
		})
	}
}

// B1 (owner decision 2026-10-10): a rejected ISBN refuses another
// provider's record of that ISBN, in either ISBN form.
func TestAutoFetch_ISBNRejectionRefusesOtherProvider(t *testing.T) {
	for _, fetch := range autoFetchRuns {
		t.Run(fetch.label, func(t *testing.T) {
			rejected := metadata.BookMetadata{Title: rejFetchTitle, Author: "Synthetic Penwright", ISBN10: "0306406152", Description: "rejected description"}
			sameISBN := metadata.BookMetadata{Title: rejFetchTitle, Author: "Synthetic Penwright", ISBN13: "9780306406157", Description: "other provider description"}
			a := &verdictSource{name: "Audible", results: []metadata.BookMetadata{rejected}}
			b := &verdictSource{name: "Synthetic Source B", results: []metadata.BookMetadata{sameISBN}}
			f, id := rejFetchFixture(t, a, b)
			rejectFor(t, f.store, id, "Audible", rejected)

			_, err := fetch.run(f.mfs, id)
			require.ErrorIs(t, err, ErrOwnerRejectedNotApplied)
			require.Nil(t, f.book(id).Description)
		})
	}
}

// rejectingSource records the owner's rejection of its own record while it
// is being searched: a rejection made while the providers answer.
type rejectingSource struct {
	*verdictSource
	onSearch func()
}

func (r *rejectingSource) SearchByTitle(ctx context.Context, q string) ([]metadata.BookMetadata, error) {
	r.onSearch()
	return r.verdictSource.SearchByTitle(ctx, q)
}

func (r *rejectingSource) SearchByTitleAndAuthor(ctx context.Context, t, a string) ([]metadata.BookMetadata, error) {
	r.onSearch()
	return r.verdictSource.SearchByTitleAndAuthor(ctx, t, a)
}

// S2 (round 3): the rejections are read again just before the write, so a
// rejection made during the search is honoured.
func TestAutoFetch_RejectionDuringSearchIsHonoured(t *testing.T) {
	for _, fetch := range autoFetchRuns {
		t.Run(fetch.label, func(t *testing.T) {
			rec := rejFetchRecord("B0SYNREJ01", "rejected description")
			src := &rejectingSource{verdictSource: &verdictSource{name: "Audible", results: []metadata.BookMetadata{rec}}}
			f, id := rejFetchFixture(t, src)
			var once sync.Once
			src.onSearch = func() { once.Do(func() { rejectFor(t, f.store, id, "Audible", rec) }) }

			_, err := fetch.run(f.mfs, id)
			require.ErrorIs(t, err, ErrOwnerRejectedNotApplied)
			require.Nil(t, f.book(id).Description, "a rejection made during the search was not honoured")
		})
	}
}

// S2 (round 3): ApplyMetadataCandidateWithOptions, the choke point every
// candidate apply goes through, refuses a rejected candidate whatever the
// options, including a record rejected under another source with the same
// ASIN.
func TestApplyCandidate_RefusesOwnerRejected(t *testing.T) {
	rec := rejFetchRecord("B0SYNREJ01", "rejected description")
	for _, tc := range []struct {
		name string
		opts ApplyOptions
		cand MetadataCandidate
	}{
		{"hand-picked", ApplyOptions{}, MetadataCandidate{Source: "Audible", Title: rejFetchTitle, ASIN: "B0SYNREJ01", Description: "rejected description"}},
		{"fill-only", ApplyOptions{FillOnly: true}, MetadataCandidate{Source: "Audible", Title: rejFetchTitle, ASIN: "B0SYNREJ01", Description: "rejected description"}},
		{"owner-reviewed", ApplyOptions{OwnerReviewed: true}, MetadataCandidate{Source: "Audible", Title: rejFetchTitle, ASIN: "B0SYNREJ01", Description: "rejected description"}},
		{"Audnexus, same ASIN", ApplyOptions{}, MetadataCandidate{Source: audnexusSource, Title: rejFetchTitle, ASIN: "B0SYNREJ01", Description: "rejected description"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, id := rejFetchFixture(t)
			rejectFor(t, f.store, id, "Audible", rec)
			_, err := f.mfs.ApplyMetadataCandidateWithOptions(id, tc.cand, nil, tc.opts)
			require.ErrorIs(t, err, ErrOwnerRejectedNotApplied)
			require.Nil(t, f.book(id).Description)
		})
	}
}

// S2 (round 3): the rejection re-read runs under the book's write stripe at
// the commit, so a rejection landing after the apply started still wins.
func TestApplyCandidate_RejectionDuringApplyRefusedAtCommit(t *testing.T) {
	f, id := rejFetchFixture(t)
	cand := MetadataCandidate{Source: "Audible", Title: rejFetchTitle, ASIN: "B0SYNREJ01", Description: "rejected description"}
	calls := 0
	guard := func(*database.Book) error {
		calls++
		if calls == 2 {
			// The second call is the commit guard, under the book's write
			// stripe, after every earlier check passed. The rejection key is
			// written raw (RecordRejection would wait for the stripe this
			// call holds); the owner-rejection guard runs after this one.
			key := RejectedCandidateStoreKey(id, cand.Source, cand.Title)
			require.NoError(t, f.store.SetRaw(key, RejectionValue(nil, CandidateRejectionIdentifiers(&cand))))
		}
		return nil
	}
	_, err := f.mfs.ApplyMetadataCandidateWithOptions(id, cand, nil, ApplyOptions{Guard: guard})
	require.Equal(t, 2, calls, "fixture: the guard must reach the commit")
	require.ErrorIs(t, err, ErrOwnerRejectedNotApplied)
	require.Nil(t, f.book(id).Description, "the rejection made during the apply was not honoured at the commit")
}

// S2: the author credit is written before the commit (applyAuthorCredit,
// under the book_authors lock). A commit refused under the write stripe --
// the owner's rejection made meanwhile, or the caller's own guard -- takes
// it back, so the book keeps exactly the credits it had and no credit is
// left that no history row names.
func TestApplyCandidate_RefusedCommitLeavesNoAuthorCredit(t *testing.T) {
	errCallerRefused := errors.New("caller guard refused at the commit")
	for name, atCommit := range map[string]func(*verdictFixture, string, *MetadataCandidate) error{
		"owner rejection made during the apply": func(f *verdictFixture, id string, cand *MetadataCandidate) error {
			key := RejectedCandidateStoreKey(id, cand.Source, cand.Title)
			require.NoError(t, f.store.SetRaw(key, RejectionValue(nil, CandidateRejectionIdentifiers(cand))))
			return nil
		},
		"caller guard": func(*verdictFixture, string, *MetadataCandidate) error { return errCallerRefused },
	} {
		t.Run(name, func(t *testing.T) {
			f, id := rejFetchFixture(t)
			kept, err := f.store.CreateAuthor("Synthetic Kept Author")
			require.NoError(t, err)
			require.NoError(t, f.store.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: kept.ID, Role: "author", Position: 0}}))
			_, err = f.store.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &kept.ID; return nil })
			require.NoError(t, err)
			before, err := f.store.GetBookAuthors(id)
			require.NoError(t, err)

			cand := MetadataCandidate{Source: "Audible", Title: rejFetchTitle, ASIN: "B0SYNCRD01", Author: "Synthetic Added Author", Description: "d"}
			calls := 0
			var atGuard []database.BookAuthor
			guard := func(*database.Book) error {
				calls++
				if calls == 2 {
					atGuard, _ = f.store.GetBookAuthors(id)
					return atCommit(f, id, &cand)
				}
				return nil
			}
			_, err = f.mfs.ApplyMetadataCandidateWithOptions(id, cand, nil, ApplyOptions{Guard: guard})
			require.Error(t, err)
			require.Equal(t, 2, calls, "fixture: the guard must reach the commit")
			require.Len(t, atGuard, len(before)+1, "fixture: the credit must be written before the commit for the test to mean anything")

			after, err := f.store.GetBookAuthors(id)
			require.NoError(t, err)
			require.True(t, sameAuthorCredits(before, after), "refused apply left author credits: before %+v, after %+v", before, after)
			require.Nil(t, f.book(id).Description)
		})
	}
}

// Review round 4, S1: the bulk-fetch handler picks a candidate with the
// rejections as read after its search, assembles the fields, and commits
// through CommitCandidateApply. A rejection the owner makes between the
// pick and the commit refuses the write; without one the same commit lands.
func TestCommitCandidateApply_RefusesRejectionMadeAfterThePick(t *testing.T) {
	cand := MetadataCandidate{Source: "Audible", Title: rejFetchTitle, ASIN: "B0SYNBLK01", ISBN13: "9780306406157"}
	for _, tc := range []struct {
		name     string
		rejectAt func(*verdictFixture, string)
		wantErr  error
	}{
		{"rejected after the pick", func(f *verdictFixture, id string) { require.NoError(t, RecordRejection(f.store, id, &cand)) }, ErrOwnerRejectedNotApplied},
		{"another source rejected its ISBN after the pick", func(f *verdictFixture, id string) {
			require.NoError(t, RecordRejection(f.store, id, &MetadataCandidate{Source: "Open Library", Title: "Other", ISBN10: "0306406152"}))
		}, ErrOwnerRejectedNotApplied},
		{"nothing rejected", func(*verdictFixture, string) {}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, id := rejFetchFixture(t)
			before := f.book(id)
			picked, err := LoadRejectedCandidates(f.store, id)
			require.NoError(t, err)
			require.False(t, picked.HasCandidate(&cand), "fixture: the candidate must be acceptable at the pick")

			tc.rejectAt(f, id)
			working := *before
			working.Publisher = new("Synthetic Bulk Pub")
			updated, err := f.mfs.CommitCandidateApply(id, before, &working, nil, &cand)
			if tc.wantErr == nil {
				require.NoError(t, err)
				require.NotNil(t, updated)
				require.Equal(t, "Synthetic Bulk Pub", *f.book(id).Publisher)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
			require.Nil(t, updated)
			require.Nil(t, f.book(id).Publisher, "the candidate rejected after the pick was written")
		})
	}
}

// An unreadable rejection list refuses the apply.
func TestApplyCandidate_UnreadableRejectionsFailClosed(t *testing.T) {
	f, id := rejFetchFixture(t)
	svc := NewService(failingRejectionScan{f.store})
	_, err := svc.ApplyMetadataCandidate(id, MetadataCandidate{Source: "Audible", Title: rejFetchTitle, Description: "d"}, nil)
	require.ErrorIs(t, err, ErrOwnerRejectionCheckFailed)
	require.Nil(t, f.book(id).Description)
}
