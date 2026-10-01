// file: internal/server/metadata_bulk_fetch_log_test.go
// version: 1.5.0
// guid: 8966af00-704c-4a19-99e8-b832e27d9f7c
// last-edited: 2026-10-01

package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
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
// checks the op log and the ledger: a book with no usable title of its own is
// searched by its transcribed title, then its folder name, and is skipped --
// with its own line and a ledger row naming why -- only when neither is
// usable. The fallback is a query only: no book's title changes.
func TestBulkFetchByIDs_LogsPerBookOutcomes(t *testing.T) {
	disableMetadataSourcesForTest(t)

	books := map[string]*database.Book{
		"b-real":   {ID: "b-real", Title: "Dune Messiah"},
		"b-frag":   {ID: "b-frag", Title: "06 Chapter 6"},
		"b-empty":  {ID: "b-empty", Title: ""},
		"b-ph":     {ID: "b-ph", Title: "Unknown Title", FilePath: "/library/Unknown Author/Unknown Title/book.m4b"},
		"b-trans":  {ID: "b-trans", Title: "", TranscribedTitle: strPtr("Marvel's Planet Hulk")},
		"b-folder": {ID: "b-folder", Title: "Chapter 3", FilePath: "/library/Paolini/Eldest/03.mp3"},
	}
	store, _, _ := newFastpathMockStore(books, nil)
	var ledgerMu sync.Mutex
	ledger := map[string]string{}
	store.CreateOperationResultFunc = func(r *database.OperationResult) error {
		ledgerMu.Lock()
		ledger[r.BookID] = r.Status
		ledgerMu.Unlock()
		return nil
	}
	srv := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	rec := &recordingProgress{}

	ids := []string{"b-real", "b-frag", "b-empty", "b-ph", "b-trans", "b-folder"}
	if _, err := srv.runBulkMetadataFetchForBookIDs(context.Background(), "op-bulk-log",
		ids, operations.BulkMetadataFetchParams{}, store, rec); err != nil {
		t.Fatalf("run: %v", err)
	}

	noMatch := rec.withPrefix("no match: ")
	if len(noMatch) != 3 {
		t.Fatalf("no-match lines = %v, want 3", noMatch)
	}
	for _, want := range []string{
		`"Dune Messiah"`,
		`searched transcribed_title "Marvel's Planet Hulk"`,
		`searched folder_title "Eldest"`,
	} {
		found := false
		for _, l := range noMatch {
			found = found || strings.Contains(l.msg, want)
		}
		if !found {
			t.Errorf("no no-match line contains %q: %v", want, noMatch)
		}
	}
	skipped := rec.withPrefix("skipped: ")
	if len(skipped) != 3 {
		t.Fatalf("skipped lines = %v, want 3 (fragment, empty, placeholder)", skipped)
	}
	for _, want := range []string{"chapter fragment", "empty title", "placeholder title"} {
		found := false
		for _, l := range skipped {
			found = found || (strings.Contains(l.msg, want) && strings.Contains(l.msg, "no usable title"))
		}
		if !found {
			t.Errorf("no skipped line names %q with its reason: %v", want, skipped)
		}
	}
	wantLedger := map[string]string{
		"b-real": metafetch.FetchStatusNotFound, "b-trans": metafetch.FetchStatusNotFound, "b-folder": metafetch.FetchStatusNotFound,
		"b-frag": metafetch.FetchStatusSkippedFragment, "b-empty": metafetch.FetchStatusSkippedNoTitle, "b-ph": metafetch.FetchStatusSkippedNoTitle,
	}
	for id, want := range wantLedger {
		if ledger[id] != want {
			t.Errorf("ledger[%s] = %q, want %q", id, ledger[id], want)
		}
	}
	if want := "complete 6/6 — found 0, not found 3, skipped 3, errors 0"; rec.last() != want {
		t.Errorf("final progress = %q, want %q", rec.last(), want)
	}
	for id, title := range map[string]string{"b-trans": "", "b-folder": "Chapter 3", "b-ph": "Unknown Title"} {
		if books[id].Title != title {
			t.Errorf("book %s title changed to %q; the fallback is only a query", id, books[id].Title)
		}
	}
}

// resolveBulkFetchQuery keys the fetch cache on the stand-in title (the same
// identity metafetch's per-book search writes), and leaves a book with a real
// title on the identity the pre-loop computed.
func TestResolveBulkFetchQuery_IdentityFollowsStandIn(t *testing.T) {
	asin := "B00TESTASN"
	books := map[string]*database.Book{
		"b-trans": {ID: "b-trans", Title: "Unknown Title", TranscribedTitle: strPtr("Planet Hulk"), ASIN: &asin},
	}
	store, _, _ := newFastpathMockStore(books, nil)

	q := resolveBulkFetchQuery(store, "b-trans", "Unknown Title", "", "Unknown Author", "", nil, nil, nil)
	if !q.query.Usable || q.query.Title != "Planet Hulk" {
		t.Fatalf("query = %+v, want Planet Hulk", q.query)
	}
	if want := metafetch.FetchCacheIdentity("Planet Hulk", "Unknown Author", &asin, nil, nil); q.identity != want {
		t.Errorf("identity = %q, want the stand-in's %q", q.identity, want)
	}

	q = resolveBulkFetchQuery(store, "b-real", "Eldest", "", "Christopher Paolini", "pre-loop-identity", nil, nil, nil)
	if q.query.Title != "Eldest" || q.identity != "pre-loop-identity" {
		t.Errorf("real title: %+v identity %q, want unchanged", q.query, q.identity)
	}
}

// A folder-evidence title ("Apollo 13", "Cobra 100 of 151") goes to the
// resolver without a GetBookByID, keeps the pre-loop identity when it is
// searched by its own title, and is skipped as a sibling part beside its
// counted siblings.
func TestResolveBulkFetchQuery_FolderEvidenceTitles(t *testing.T) {
	gets := 0
	store := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { gets++; return nil, nil },
		LiveBookPathsUnderDirFunc: func(dir string) (map[string]string, error) {
			if dir != "/library/Authors/Timothy Zahn/Cobra" {
				return map[string]string{}, nil
			}
			return map[string]string{
				"b-cobra": dir + "/Cobra 100 of 151.mp3",
				"s1":      dir + "/Cobra 099 of 151.mp3",
				"s2":      dir + "/Cobra 101 of 151.mp3",
				// No duration is known here, so the set must be big (5+).
				"s3": dir + "/Cobra 102 of 151.mp3",
				"s4": dir + "/Cobra 103 of 151.mp3",
				"s5": dir + "/Cobra 104 of 151.mp3",
			}, nil
		},
	}
	memo := metabatch.NewFolderMemo()
	q := resolveBulkFetchQuery(store, "b-apollo", "Apollo 13", "/library/Authors/Jim Lovell/Apollo 13.m4b", "Jim Lovell", "pre-loop-identity", nil, nil, memo)
	if !q.query.Usable || q.query.Title != "Apollo 13" || q.identity != "pre-loop-identity" {
		t.Errorf("Apollo 13: %+v identity %q, want its own title on the pre-loop identity", q.query, q.identity)
	}
	q = resolveBulkFetchQuery(store, "b-cobra", "Cobra 100 of 151", "/library/Authors/Timothy Zahn/Cobra/Cobra 100 of 151.mp3", "Timothy Zahn", "pre-loop-identity", nil, nil, memo)
	if q.query.Usable || q.skipKind != metabatch.SkipKindSiblingPart || q.skipStatus != metafetch.FetchStatusSkippedFragment {
		t.Errorf("Cobra 100 of 151: %+v kind %q status %q, want a sibling-part skip", q.query, q.skipKind, q.skipStatus)
	}
	if gets != 0 {
		t.Errorf("GetBookByID called %d times, want 0", gets)
	}
}

// E2: with no full book, the folder-evidence branch builds the book from
// the walk's BookCore, so the ASIN reaches the identity (the same identity
// the full-book path computes) and the duration reaches the part test.
func TestResolveBulkFetchQuery_FolderEvidenceUsesTheCore(t *testing.T) {
	const dir = "/library/Authors/Timothy Zahn/Cobra"
	store := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { t.Fatal("GetBookByID called"); return nil, nil },
		LiveBookPathsUnderDirFunc: func(string) (map[string]string, error) {
			return map[string]string{"b": dir + "/Cobra 100 of 151.mp3", "s1": dir + "/Cobra 099 of 151.mp3",
				"s2": dir + "/Cobra 101 of 151.mp3", "s3": dir + "/American Gods.m4b"}, nil
		},
	}
	asin := "B00TESTASN"
	core := database.BookCore{ID: "b", Title: "American Gods [64k 577MB]", FilePath: "/library/Authors/Neil Gaiman/American Gods [64k 577MB].m4b", ASIN: &asin}
	full := core.ToBook()
	viaCore := resolveBulkFetchQuery(store, "b", core.Title, core.FilePath, "Neil Gaiman", "pre", nil, &core, nil)
	viaFull := resolveBulkFetchQuery(store, "b", core.Title, core.FilePath, "Neil Gaiman", "pre", &full, nil, nil)
	if !viaCore.query.Usable || viaCore.query.Title != "American Gods" || viaCore.identity != viaFull.identity {
		t.Fatalf("core %+v %q, full %+v %q: want the cleaned title on one identity", viaCore.query, viaCore.identity, viaFull.query, viaFull.identity)
	}
	if want := metafetch.FetchCacheIdentity("American Gods", "Neil Gaiman", &asin, nil, nil); viaCore.identity != want {
		t.Errorf("identity = %q, want the ASIN-bearing %q", viaCore.identity, want)
	}

	long := 11 * 3600
	cobra := database.BookCore{ID: "b", Title: "Cobra 100 of 151", FilePath: dir + "/Cobra 100 of 151.mp3", Duration: &long}
	if q := resolveBulkFetchQuery(store, "b", cobra.Title, cobra.FilePath, "Timothy Zahn", "pre", nil, &cobra, nil); !q.query.Usable {
		t.Errorf("an 11 h row is a whole product, got %+v", q.query)
	}
	short := 20 * 60
	cobra.Duration = &short
	if q := resolveBulkFetchQuery(store, "b", cobra.Title, cobra.FilePath, "Timothy Zahn", "pre", nil, &cobra, nil); q.query.Usable {
		t.Errorf("a 20 min row beside its counted siblings is a part, got %+v", q.query)
	}
}

// TestBulkFetchOutcomeLine_FoundAndError covers the two outcomes the
// provider-less run above cannot reach.
func TestBulkFetchOutcomeLine_FoundAndError(t *testing.T) {
	level, msg := bulkFetchOutcomeLine(bulkFetchBook{title: "Dune", author: "Frank Herbert"}, metafetch.ChainOutcome{
		Results:    []metadata.BookMetadata{{Title: "Dune", Author: "Frank Herbert"}, {Title: "Dune Messiah"}},
		SourceName: "Audible",
		Variant:    "Dune",
	}, metafetch.FetchStatusCached)
	want := `found: "Dune" by "Frank Herbert" → 2 candidate(s) from Audible, top "Dune" by "Frank Herbert" via title variant "Dune"`
	if level != "info" || msg != want {
		t.Errorf("found line = (%s) %q, want (info) %q", level, msg, want)
	}

	level, msg = bulkFetchOutcomeLine(bulkFetchBook{title: "Dune"}, metafetch.ChainOutcome{
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

func strPtr(s string) *string { return &s }
