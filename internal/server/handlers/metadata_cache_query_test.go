// file: internal/server/handlers/metadata_cache_query_test.go
// version: 1.1.0
// guid: 6c1f0e2a-9b47-4d35-8e60-2a7d4c9b1f38
// last-edited: 2026-10-10

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// goldenBookID is a ULID-length synthetic book id.
func goldenBookID(i int) string { return fmt.Sprintf("01J0GOLDEN0000000000000%03d", i) }

// goldenReviewStore seeds a Pebble store with a fixed, fully deterministic
// review set: preset book ids, fixed timestamps far from the TTL boundary
// (2000 = always stale, 2100 = always fresh), every review status, rows with
// no candidate, an undecodable row, a deferred row, multi-file books and a
// series held only by id. Nothing reads the clock, so the responses over it
// are byte-stable and can be pinned by hash.
func goldenReviewStore(t testing.TB) (*database.PebbleStore, *metafetch.Service) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "golden-db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	future := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	statuses := []string{"", "", "no_match", "matched", "audio_confirmed", ""}
	langs := []string{"en", "English", "", "de", "spa"}
	for i := 0; i < 24; i++ {
		id := goldenBookID(i)
		title := fmt.Sprintf("Title %06d", i)
		path := fmt.Sprintf("/lib/Author %02d/Title %06d", i%5, i)
		b := &database.Book{ID: id, Title: title, FilePath: path, Format: "m4b"}
		if st := statuses[i%len(statuses)]; st != "" {
			b.MetadataReviewStatus = &st
		}
		if l := langs[i%len(langs)]; l != "" {
			b.Language = &l
		}
		if i%7 == 0 {
			tt := fmt.Sprintf("Spoken Title %d", i)
			b.TranscribedTitle = &tt
		}
		_, err := store.CreateBook(b)
		require.NoError(t, err)
		for f := 0; f < 1+i%3; f++ {
			dur := 3000 + i*10
			if i%5 == 0 && f == 1 {
				dur = 0 // an unprobed chapter: a partial runtime
			}
			require.NoError(t, store.CreateBookFile(&database.BookFile{
				ID:          fmt.Sprintf("gf-%03d-%d", i, f),
				BookID:      id,
				FilePath:    fmt.Sprintf("%s/part%02d.m4b", path, f),
				TrackNumber: f + 1,
				Duration:    dur,
			}))
		}
		fetched := old.Add(time.Duration(i) * time.Hour)
		if i%4 == 1 {
			fetched = future.Add(time.Duration(i) * time.Hour)
		}
		entry := &database.MetadataCandidateCache{BookID: id, FetchedAt: fetched, SearchFingerprint: metafetch.FingerprintPrefix + "golden"}
		switch {
		case i%9 == 8:
			// no candidates; one of them deferred
			if i == 17 {
				entry.FallbackAttempts = map[string]database.FallbackAttempt{
					"Google Books": {Outcome: database.FallbackOutcomeDeferred},
				}
			}
		case i == 13:
			entry.Candidates = []json.RawMessage{json.RawMessage(`"not an object"`)}
		default:
			for k := 0; k < 2; k++ {
				raw, merr := json.Marshal(map[string]any{
					"title": fmt.Sprintf("%s cand %d", title, k), "author": fmt.Sprintf("Author %02d", i%5),
					"source":       []string{"Audible", "Google Books", "Open Library"}[(i+k)%3],
					"score":        0.4 + float64(i%6)/10,
					"description":  "A synthetic description.",
					"language":     []string{"English", "German", ""}[i%3],
					"duration_sec": 3000 + i*10 + k*700,
					"asin":         map[bool]string{true: fmt.Sprintf("B0%08d", i/2), false: ""}[i%2 == 0],
					"score_breakdown": map[string]any{"score": 0.5, "steps": []map[string]any{
						{"id": "base", "label": "Base", "op": "base", "operand": 0.5, "running": 0.5},
					}},
					"category_tags": []string{"Category 01"},
				})
				require.NoError(t, merr)
				entry.Candidates = append(entry.Candidates, raw)
			}
		}
		require.NoError(t, store.PutMetadataCache(entry))
	}
	_, err = store.BackfillBookAtPathIndex(t.Context())
	require.NoError(t, err)
	return store, metafetch.NewService(store)
}

// serveReviewRaw serves one review request and returns the status and body.
func serveReviewRaw(t testing.TB, h *MetadataCacheHandler, query string) (int, []byte, http.Header) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/metadata/cache/review?"+query, nil)
	h.GetCacheReviewResults(c)
	return w.Code, w.Body.Bytes(), w.Header()
}

// reviewGoldenHashes pins the bytes of every listing shape the extraction of
// buildReviewableRows touches, captured on the code BEFORE the extraction
// (task 02-PR17 step 2). Regenerate only for an intended response change:
// REVIEW_GOLDEN_PRINT=1 go test -run TestReviewQuery_IndexViewByteIdentical.
var reviewGoldenHashes = map[string]string{
	"all=true&view=index":                   "daeab0dc896d8bf936a608a9416961e336a2d285c2552c91e7eb35fa4f7cf78a",
	"all=true":                              "2f79e13970811e3ba26d192e279475f7f7f27cb8f023d8608ff5a8dcccccc9f2",
	"limit=5&offset=3":                      "f88953f6361aea5593f2faffaa49d9976e66497644b1785994d87818f96d7ae4",
	"all=true&bucket=unreviewable":          "fc12ba7a5863d07f5715b70da1e889eb1b715d548b771f0f7b64b79220e2339a",
	"bucket=unreviewable&limit=2&offset=1":  "927210916592d2e41f966d4532d91fe64f5429d17d576d1e89994354b708f3be",
	"ids=" + goldenIDList(0, 3, 13, 17, 22): "38858fc128c003ae24f7445ae22ca4a081bed98d75c19be92bb64fdeffe2c1bf",
	"view=index&ids=" + goldenIDList(1, 4):  "5b68a4adf82861d12ddd40bcecffad3a0cd5b973782d72ec01a879cc694d787e",
}

func goldenIDList(is ...int) string {
	s := ""
	for n, i := range is {
		if n > 0 {
			s += ","
		}
		s += goldenBookID(i)
	}
	return s
}

// TestReviewQuery_IndexViewByteIdentical: view=index (and the other listing
// shapes that share the extracted derivation) are byte-identical to the
// responses before the extraction.
func TestReviewQuery_IndexViewByteIdentical(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	print := os.Getenv("REVIEW_GOLDEN_PRINT") != ""
	for q, want := range reviewGoldenHashes {
		code, body, _ := serveReviewRaw(t, h, q)
		require.Equal(t, http.StatusOK, code, string(body))
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		if print {
			t.Logf("%q: %q, // %d bytes", q, got, len(body))
			continue
		}
		require.Equal(t, want, got, "response for %q changed (%d bytes): %s", q, len(body), body)
	}
}

// --- the shared fixture ----------------------------------------------------------

type queryCaseFile struct {
	Rows   []queryCaseRow `json:"rows"`
	Facets map[string]any `json:"facets"`
	Cases  []queryCase    `json:"cases"`
}

type queryCaseRow struct {
	Bucket           string  `json:"bucket"`
	Status           string  `json:"status"`
	ReviewStatus     *string `json:"review_status"`
	Stale            bool    `json:"stale"`
	FallbackDeferred bool    `json:"fallback_deferred"`
	Book             struct {
		ID               string  `json:"id"`
		Title            string  `json:"title"`
		Language         *string `json:"language"`
		DurationSeconds  int     `json:"duration_seconds"`
		RuntimeLowerSec  int     `json:"runtime_lower_bound_seconds"`
		TranscribedTitle string  `json:"transcribed_title"`
	} `json:"book"`
	Candidate *metafetch.MetadataCandidate `json:"candidate"`
}

type queryCase struct {
	Name              string            `json:"name"`
	Query             map[string]string `json:"query"`
	WantIDs           []string          `json:"want_ids"`
	WantTotal         int               `json:"want_total"`
	WantRuntimeHidden int               `json:"want_runtime_hidden"`
	ServerOnly        bool              `json:"server_only"`
	Why               string            `json:"why"`
}

func loadQueryCases(t testing.TB) queryCaseFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "review_query_cases.json"))
	require.NoError(t, err)
	var f queryCaseFile
	require.NoError(t, json.Unmarshal(raw, &f))
	return f
}

// staleCutoffBase is a lastChecked far past the TTL (stale) and one far in the
// future (fresh), so no fixture row sits near the boundary.
var (
	queryOldCheck    = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	queryFutureCheck = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

// snapshotFromCases builds a snapshot from fixture rows through the real
// snapshot-row path (decodeTally.row decodes the candidate and folds the
// title). The rows keep the fixture's order as the snapshot order (FetchedAt
// descending).
func snapshotFromCases(t testing.TB, rows []queryCaseRow) *reviewSnapshot {
	t.Helper()
	snap := &reviewSnapshot{books: map[string]*database.Book{}, orphanIDs: map[string]struct{}{}}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var dec decodeTally
	for i, fr := range rows {
		b := &database.Book{ID: fr.Book.ID, Title: fr.Book.Title, Language: fr.Book.Language, MetadataReviewStatus: fr.ReviewStatus}
		if fr.Book.TranscribedTitle != "" {
			tt := fr.Book.TranscribedTitle
			b.TranscribedTitle = &tt
		}
		lr := loadedCacheRow{
			sum:              metafetch.MetadataCacheSummary{BookID: fr.Book.ID, FetchedAt: start.Add(-time.Duration(i) * time.Minute)},
			book:             b,
			lastChecked:      queryFutureCheck,
			searchable:       true,
			fallbackDeferred: fr.FallbackDeferred,
		}
		if fr.Stale {
			lr.lastChecked = queryOldCheck
		}
		switch {
		case fr.Book.DurationSeconds > 0:
			lr.files.Runtime = database.BookRuntime{Seconds: fr.Book.DurationSeconds, Source: database.RuntimeSourceFiles, FilesCounted: 1, FilesKnown: 1}
		case fr.Book.RuntimeLowerSec > 0:
			lr.files.Runtime = database.BookRuntime{Seconds: fr.Book.RuntimeLowerSec, Source: database.RuntimeSourceFiles, FilesCounted: 2, FilesKnown: 1}
		default:
			lr.files.Runtime = database.BookRuntime{Source: database.RuntimeSourceNone}
		}
		switch fr.Status {
		case unreviewableStatusNoCandidates, unreviewableStatusResolvedNoCandidates:
		case unreviewableStatusDecodeError:
			lr.candidateCount, lr.first = 1, json.RawMessage(`"not an object"`)
		default:
			raw, err := json.Marshal(fr.Candidate)
			require.NoError(t, err)
			lr.candidateCount, lr.first = 1, raw
		}
		snap.books[fr.Book.ID] = b
		snap.rows = append(snap.rows, dec.row(lr))
	}
	return snap
}

// baseFromSnapshot is the base a request would build: the overlay with no
// changed books (no reads), then the shared derivation.
func baseFromSnapshot(t testing.TB, snap *reviewSnapshot) *reviewQueryBase {
	t.Helper()
	set, err := overlayLiveBooks(snap, nil, nil, changedIDs())
	require.NoError(t, err)
	return newReviewQueryBase(reviewBaseKey{snap: snap}, set, time.Now())
}

func mustReviewQuery(t testing.TB, params map[string]string) *ReviewQuery {
	t.Helper()
	q, err := parseReviewQuery(func(k string) string { return params[k] })
	require.NoError(t, err)
	return q
}

// mustEvaluate runs evaluateReviewQuery with a deadline no test reaches.
func mustEvaluate(t testing.TB, base *reviewQueryBase, q *ReviewQuery) *reviewResultList {
	t.Helper()
	l, err := evaluateReviewQuery(base, q, time.Now().Add(time.Hour))
	require.NoError(t, err)
	return l
}

func listIDs(l *reviewResultList) []string {
	ids := make([]string, 0, len(l.refs))
	for _, ref := range l.refs {
		row, _ := l.row(ref)
		ids = append(ids, row.sum.BookID)
	}
	return ids
}

// TestReviewQuery_MatchesSharedCases runs every case of the shared fixture
// (testdata/review_query_cases.json) through the server query. The expected
// ids were derived from the lane's TypeScript chain; 02-PR18 runs the same
// file through the lane.
func TestReviewQuery_MatchesSharedCases(t *testing.T) {
	f := loadQueryCases(t)
	require.GreaterOrEqual(t, len(f.Cases), 12)
	snap := snapshotFromCases(t, f.Rows)
	base := baseFromSnapshot(t, snap)

	// The derivation agrees with the fixture's API view of every row: status,
	// stale flag and listing order, per bucket.
	var wantReviewable, wantUnreviewable []string
	for _, fr := range f.Rows {
		if fr.Bucket == reviewBucketReviewable {
			wantReviewable = append(wantReviewable, fr.Book.ID)
		} else {
			wantUnreviewable = append(wantUnreviewable, fr.Book.ID)
		}
	}
	byID := map[string]queryCaseRow{}
	for _, fr := range f.Rows {
		byID[fr.Book.ID] = fr
	}
	var gotReviewable, gotUnreviewable []string
	for _, r := range base.rows.reviewable {
		fr := byID[r.row.sum.BookID]
		require.Equal(t, fr.Status, r.status, r.row.sum.BookID)
		require.Equal(t, fr.Stale, r.stale, r.row.sum.BookID)
		gotReviewable = append(gotReviewable, r.row.sum.BookID)
	}
	for _, u := range base.rows.unreviewable {
		fr := byID[u.row.sum.BookID]
		require.Equal(t, fr.Status, u.status, u.row.sum.BookID)
		require.Equal(t, fr.Stale, u.stale, u.row.sum.BookID)
		gotUnreviewable = append(gotUnreviewable, u.row.sum.BookID)
	}
	require.Equal(t, wantReviewable, gotReviewable)
	require.Equal(t, wantUnreviewable, gotUnreviewable)

	for _, tc := range f.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			q := mustReviewQuery(t, tc.Query)
			l := mustEvaluate(t, base, q)
			got := listIDs(l)
			if got == nil {
				got = []string{}
			}
			require.Equal(t, tc.WantIDs, got, tc.Why)
			require.Equal(t, tc.WantTotal, len(l.refs))
			require.Equal(t, tc.WantRuntimeHidden, l.runtimeHidden)

			// The facets are the unfiltered chip counts, the same for every case.
			facets := reviewFacets(l)
			raw, err := json.Marshal(facets)
			require.NoError(t, err)
			var gotFacets map[string]any
			require.NoError(t, json.Unmarshal(raw, &gotFacets))
			for k, want := range f.Facets {
				require.Equal(t, want, gotFacets[k], "facet %s", k)
			}
			require.EqualValues(t, tc.WantRuntimeHidden, gotFacets["runtime_hidden"])
		})
	}
}

// TestReviewQuery_GrammarCorpus runs the 02-PR3 conformance corpus through
// the q path: each value as the whole Title box, over a one-row snapshot
// whose title is the corpus input.
func TestReviewQuery_GrammarCorpus(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "querygrammar", "testdata", "conformance.json"))
	require.NoError(t, err)
	var corpus []struct {
		Name            string   `json:"name"`
		Pattern         string   `json:"pattern"`
		Quoted          bool     `json:"quoted"`
		Input           string   `json:"input"`
		WantMatch       *bool    `json:"want_match"`
		WantError       bool     `json:"want_error"`
		GoErrorContains string   `json:"go_error_contains"`
		Engines         []string `json:"engines"`
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	ran := 0
	for _, c := range corpus {
		if len(c.Engines) > 0 && !slices.Contains(c.Engines, "go") {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			if c.Pattern == "" {
				// An empty Title box is no filter at all (the lane's
				// compileTitleFilter rule), not the grammar's empty-value
				// error, so this row has no q-path equivalent.
				_, err := compileReviewTitleFilter("")
				require.NoError(t, err)
				return
			}
			// The value as the whole box: a quoted value keeps its quotes;
			// an unquoted one is the bare box, which the lane's parser
			// passes through whole (no field token in it).
			box := c.Pattern
			if c.Quoted {
				box = `"` + c.Pattern + `"`
			}
			// The box must reach the grammar as exactly this value.
			_, filters := parseSearchFilters(box)
			require.Empty(t, filters, "corpus value %q reads as a field token", box)
			require.Equal(t, box, strings.TrimSpace(box), "corpus value %q would be trimmed", box)

			q, err := parseReviewQuery(func(k string) string {
				if k == "q" {
					return box
				}
				return ""
			})
			if c.WantError {
				require.Error(t, err)
				require.Contains(t, err.Error(), c.GoErrorContains)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, c.WantMatch)
			title := c.Input
			snap := snapshotFromCases(t, []queryCaseRow{oneTitleRow(title)})
			l := mustEvaluate(t, baseFromSnapshot(t, snap), q)
			require.Equal(t, *c.WantMatch, len(l.refs) == 1, "pattern %q quoted=%v input %q", c.Pattern, c.Quoted, c.Input)
		})
		ran++
	}
	t.Logf("corpus rows run through the q path: %d", ran)
	require.Positive(t, ran)
}

func oneTitleRow(title string) queryCaseRow {
	var r queryCaseRow
	r.Bucket, r.Status = reviewBucketReviewable, reviewStatusMatched
	r.Book.ID, r.Book.Title = "c01", title
	r.Candidate = &metafetch.MetadataCandidate{Source: "Audible", Title: "Work", Author: "Author 01", Score: 0.9}
	return r
}

// TestReviewQuery_ChipPausesOtherFilters: a chip view lists the same rows
// whatever the other switches say, and only the title narrows it.
func TestReviewQuery_ChipPausesOtherFilters(t *testing.T) {
	f := loadQueryCases(t)
	base := baseFromSnapshot(t, snapshotFromCases(t, f.Rows))
	for _, chip := range reviewChips {
		bare := listIDs(mustEvaluate(t, base, mustReviewQuery(t, map[string]string{"chip": chip})))
		loaded := listIDs(mustEvaluate(t, base, mustReviewQuery(t, map[string]string{
			"chip": chip, "hide_applied": "true", "hide_rejected": "true", "hide_no_match": "true",
			"hide_runtime": "true", "match_language": "true", "has_transcription": "true",
			"transcription_matched": "true", "hide_multi_book": "true", "min_confidence": "99",
			"source": "audible", "hide_skipped": "true", "skipped": "b01,u01",
		})))
		require.Equal(t, bare, loaded, chip)
		require.NotEmpty(t, bare, chip)
	}
	// The title still narrows a chip.
	narrowed := listIDs(mustEvaluate(t, base, mustReviewQuery(t, map[string]string{"chip": "total", "q": "chapter"})))
	require.Equal(t, []string{"b07", "b08"}, narrowed)
}

// --- the ported rules, from the TypeScript cases -----------------------------

func TestReviewQuery_RuntimeRule(t *testing.T) {
	complete := func(sec int) metabatch.BookFileFacts {
		return metabatch.BookFileFacts{Runtime: database.BookRuntime{Seconds: sec, Source: database.RuntimeSourceFiles, FilesCounted: 1, FilesKnown: 1}}
	}
	partial := func(sec int) metabatch.BookFileFacts {
		return metabatch.BookFileFacts{Runtime: database.BookRuntime{Seconds: sec, Source: database.RuntimeSourceFiles, FilesCounted: 2, FilesKnown: 1}}
	}
	cases := []struct {
		name  string
		cand  *metafetch.MetadataCandidate
		facts metabatch.BookFileFacts
		want  bool
	}{
		// rowState.test.ts runtimeHiddenBySwitch.
		{"hides a warned row the 10% rule alone would keep", &metafetch.MetadataCandidate{DurationSec: 36000 - 3180, DurationDeltaSec: 3180}, complete(36000), true},
		{"hides a row over 10% off", &metafetch.MetadataCandidate{DurationSec: 3500}, complete(3000), true},
		{"keeps a row within ten minutes and within 10%", &metafetch.MetadataCandidate{DurationSec: 35700, DurationDeltaSec: 300}, complete(36000), false},
		{"keeps a row whose runtime is unknown", nil, metabatch.BookFileFacts{}, false},
		// rowState.test.ts runtimeDiffers, through the switch.
		{"warns strictly above ten minutes (at)", &metafetch.MetadataCandidate{DurationDeltaSec: 600}, metabatch.BookFileFacts{}, false},
		{"warns strictly above ten minutes (over)", &metafetch.MetadataCandidate{DurationDeltaSec: 601}, metabatch.BookFileFacts{}, true},
		{"symmetric delta", &metafetch.MetadataCandidate{DurationDeltaSec: -900}, metabatch.BookFileFacts{}, true},
		// runtimeDiffersFromBook's doc: partial runtimes prove one direction only.
		{"partial lower bound above the candidate by over 10%", &metafetch.MetadataCandidate{DurationSec: 10000}, partial(20000), true},
		{"partial lower bound below the candidate proves nothing", &metafetch.MetadataCandidate{DurationSec: 30000}, partial(20000), false},
		{"exactly 10% is not over", &metafetch.MetadataCandidate{DurationSec: 3300}, complete(3000), false},
		{"a failed file read is an unknown runtime", &metafetch.MetadataCandidate{DurationSec: 100}, metabatch.BookFileFacts{Runtime: database.BookRuntime{Seconds: 9000, Source: database.RuntimeSourceFiles, FilesCounted: 1, FilesKnown: 1}, Err: errors.New("read failed")}, false},
		{"no candidate runtime", &metafetch.MetadataCandidate{}, complete(3000), false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, reviewRuntimeHidden(tc.cand, tc.facts), tc.name)
	}
}

func TestReviewQuery_LanguageRule(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		book *string
		cand string
		hide bool
	}{
		{s("en"), "English", false},
		{s("eng"), "en", false},
		{s("de"), "English", true},
		{nil, "fr", false},     // unknown book language: no-op
		{s("en"), "", false},   // unknown candidate language: no-op
		{s("  "), "de", false}, // blank is unknown
		{s("Klingon"), "klingon", false},
		{s("Mandarin"), "zho", false},
		{s("spa"), "French", true},
	}
	for _, tc := range cases {
		require.Equal(t, tc.hide, reviewLanguageHidden(tc.book, normalizeReviewLanguage(tc.cand)), "%v vs %q", tc.book, tc.cand)
	}
}

func TestReviewQuery_CandidateKeyAndProvider(t *testing.T) {
	require.Equal(t, "asin:B1", reviewCandidateKey(&metafetch.MetadataCandidate{ASIN: "B1", ISBN: "9", Source: "Audible"}))
	require.Equal(t, "isbn:9", reviewCandidateKey(&metafetch.MetadataCandidate{ISBN: "9", Source: "Audible"}))
	require.Equal(t, "Audible:a title:an author", reviewCandidateKey(&metafetch.MetadataCandidate{Source: "Audible", Title: " A Title ", Author: "An Author "}))
	for in, want := range map[string]string{
		"Audible": "audible", "audnexus (audible)": "audible", " Google Books ": "google_books",
		"google": "google_books", "Open Library": "openlibrary", "openlibrary": "openlibrary",
		"Hardcover": "hardcover", "": "",
	} {
		require.Equal(t, want, reviewProviderID(in), in)
	}
}

// --- the handler path ----------------------------------------------------------------

type pageResp struct {
	Data struct {
		Results    []metabatch.CandidateResult `json:"results"`
		TotalCount int                         `json:"total_count"`
		Generation string                      `json:"generation"`
		Facets     map[string]any              `json:"facets"`
		IDs        []string                    `json:"ids"`
		Total      int                         `json:"total"`
	} `json:"data"`
}

func servePage(t testing.TB, h *MetadataCacheHandler, query string) (pageResp, http.Header, int) {
	t.Helper()
	code, body, hdr := serveReviewRaw(t, h, "view=page&"+query)
	require.Equal(t, http.StatusOK, code, string(body))
	var r pageResp
	require.NoError(t, json.Unmarshal(body, &r))
	return r, hdr, len(body)
}

// TestReviewQuery_IDsAllAfterPageHitsLRU: a page, the next page and ids=all
// for one filter are one evaluation.
func TestReviewQuery_IDsAllAfterPageHitsLRU(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)

	p1, _, _ := servePage(t, h, "hide_applied=true&limit=5")
	afterPage := h.reviewQuery.evaluations.Load()
	require.EqualValues(t, 1, afterPage)
	p2, _, _ := servePage(t, h, "hide_applied=true&limit=5&offset=5")
	ids, _, _ := servePage(t, h, "hide_applied=true&ids=all")
	delta := h.reviewQuery.evaluations.Load() - afterPage
	t.Logf("evaluations after the first page: %d; added by page 2 and ids=all: %d", afterPage, delta)
	require.Zero(t, delta)
	require.Equal(t, p1.Data.TotalCount, ids.Data.Total)
	require.Len(t, ids.Data.IDs, ids.Data.Total)
	require.Equal(t, p1.Data.Generation, ids.Data.Generation)
	for i, r := range append(p1.Data.Results, p2.Data.Results...) {
		require.Equal(t, ids.Data.IDs[i], r.Book.ID)
	}
	// A different filter is a new evaluation over the same overlay.
	servePage(t, h, "hide_no_match=true")
	require.EqualValues(t, 2, h.reviewQuery.evaluations.Load())
	require.EqualValues(t, 1, h.reviewQuery.bases.Load())
}

// TestReviewQuery_WriteMissesLRU: a metadata-cache write and a book write
// each move a generation, and each forces a re-evaluation. The generation
// label names the data the list was built from: while the old snapshot is
// still served (stale-while-revalidate) the label stays the old one, and it
// changes, with the rows, once the rebuild lands.
func TestReviewQuery_WriteMissesLRU(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	h.reviewSnap.minInterval = 0 // the write's rebuild starts at once
	const query = "hide_applied=true"

	first, _, _ := servePage(t, h, query)
	firstIDs, _, _ := servePage(t, h, query+"&ids=all")
	require.EqualValues(t, 1, h.reviewQuery.evaluations.Load())
	require.Contains(t, firstIDs.Data.IDs, goldenBookID(0))

	// A cache write: book 0's entry loses its candidates, so it leaves the
	// reviewable list once the snapshot reflects the write.
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: goldenBookID(0), FetchedAt: time.Date(2000, 2, 1, 0, 0, 0, 0, time.UTC)}))
	afterCache, _, _ := servePage(t, h, query+"&ids=all")
	require.EqualValues(t, 2, h.reviewQuery.evaluations.Load(), "the write moved the live generation: the LRU missed")
	rebuilt := fmt.Sprintf("%d.", store.MetadataCacheGeneration())
	deadline := time.Now().Add(10 * time.Second)
	for !strings.HasPrefix(afterCache.Data.Generation, rebuilt) {
		// Still the old snapshot: the old rows, so the old label.
		require.Equal(t, first.Data.Generation, afterCache.Data.Generation)
		require.Contains(t, afterCache.Data.IDs, goldenBookID(0))
		require.True(t, time.Now().Before(deadline), "the snapshot rebuild never landed")
		time.Sleep(10 * time.Millisecond)
		afterCache, _, _ = servePage(t, h, query+"&ids=all")
	}
	require.NotEqual(t, first.Data.Generation, afterCache.Data.Generation)
	require.NotContains(t, afterCache.Data.IDs, goldenBookID(0), "the rebuilt page reflects the write")
	require.Equal(t, len(firstIDs.Data.IDs)-1, afterCache.Data.Total)

	// A book write. A title is an identity field, so the same batch deletes
	// the book's cache row (MetadataCacheGeneration's contract) and a
	// snapshot rebuild would drop the row; rebuilds are held off here so
	// what is asserted is the overlay over the current snapshot.
	h.reviewSnap.mu.Lock()
	h.reviewSnap.minInterval = time.Hour
	h.reviewSnap.mu.Unlock()
	_, err := store.ModifyBook(goldenBookID(1), func(b *database.Book) error {
		b.Title = "Title 000001 Retitled"
		return nil
	})
	require.NoError(t, err)
	before := h.reviewQuery.evaluations.Load()
	sameQuery, _, _ := servePage(t, h, query+"&ids=all")
	require.Equal(t, before+1, h.reviewQuery.evaluations.Load(), "the book write moved the live generation: the LRU missed")
	// The book half of the label is the book generation the overlay read.
	require.NotEqual(t, afterCache.Data.Generation, sameQuery.Data.Generation)
	afterBook, _, _ := servePage(t, h, query+"&q=retitled")
	require.Equal(t, sameQuery.Data.Generation, afterBook.Data.Generation)
	require.True(t, strings.HasPrefix(afterBook.Data.Generation, rebuilt), "no rebuild ran: the cache half is the snapshot's")
	// The overlay's live title is what the filter matched.
	require.Len(t, afterBook.Data.Results, 1)
	require.Equal(t, goldenBookID(1), afterBook.Data.Results[0].Book.ID)
}

// countBuilds wraps the snapshot cache's build with a counter.
func countBuilds(c *reviewSnapshotCache) *atomic.Int64 {
	var n atomic.Int64
	c.mu.Lock()
	orig := c.build
	c.build = func(ctx context.Context, prev *reviewSnapshot) (*reviewSnapshot, error) {
		n.Add(1)
		return orig(ctx, prev)
	}
	c.mu.Unlock()
	return &n
}

// TestReviewQuery_NoBuildFromFilter: once a snapshot exists, filtering over
// an unchanged library starts no build, however many filters run.
func TestReviewQuery_NoBuildFromFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	h.reviewSnap.minInterval = 0 // a rebuild, if anything asked for one, would start at once
	servePage(t, h, "")          // the cold start: the one request that may wait on a build
	builds := countBuilds(h.reviewSnap)
	for _, q := range []string{
		"q=title", "q=/^title/", "hide_runtime=true", "chip=stale", "chip=errors&q=1",
		"hide_multi_book=true", "sort=title", "ids=all", "match_language=true&min_confidence=50", "bucket=unreviewable",
	} {
		servePage(t, h, q)
	}
	time.Sleep(50 * time.Millisecond) // a build would be on a goroutine
	t.Logf("builds started by 10 filters over an unchanged snapshot: %d", builds.Load())
	require.Zero(t, builds.Load())
}

// queryFakeStore serves books from a map and counts how many it was asked for.
type queryFakeStore struct {
	MetadataCacheBookStore
	books map[string]*database.Book
	reads *atomic.Int64
}

func (f queryFakeStore) GetBooksByIDs(ids []string) ([]database.Book, error) {
	f.reads.Add(int64(len(ids)))
	out := make([]database.Book, 0, len(ids))
	for _, id := range ids {
		if b := f.books[id]; b != nil {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (f queryFakeStore) GetBookByID(id string) (*database.Book, error) { return f.books[id], nil }

type queryFakeSvc struct{ MetadataCacheFetchService }

// TestReviewQuery_OverlayBoundedAt2000: a query whose overlay finds more than
// overlayRebuildThreshold changed books asks for a rebuild, as the listing
// does, and is served what it has; at the threshold it does not ask.
func TestReviewQuery_OverlayBoundedAt2000(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, changed := range []int{overlayRebuildThreshold, overlayRebuildThreshold + 1} {
		t.Run(strconv.Itoa(changed), func(t *testing.T) {
			rows := make([]queryCaseRow, changed+5)
			ids := make([]string, 0, changed)
			books := map[string]*database.Book{}
			for i := range rows {
				rows[i] = oneTitleRow(fmt.Sprintf("Title %06d", i))
				rows[i].Book.ID = fmt.Sprintf("q%05d", i)
				books[rows[i].Book.ID] = &database.Book{ID: rows[i].Book.ID, Title: rows[i].Book.Title}
				if i < changed {
					ids = append(ids, rows[i].Book.ID)
				}
			}
			snap := snapshotFromCases(t, rows)
			var reads atomic.Int64
			sc := newReviewSnapshotCache(func(context.Context, *reviewSnapshot) (*reviewSnapshot, error) { return snap, nil }, nil)
			sc.minInterval = 0
			h := &MetadataCacheHandler{
				store:             queryFakeStore{books: books, reads: &reads},
				svc:               queryFakeSvc{},
				reviewSnap:        sc,
				booksChangedSince: changedIDs(ids...),
				reviewQuery:       newReviewQueryCache(),
			}
			r, _, _ := servePage(t, h, "limit=3")
			require.Equal(t, len(rows), r.Data.TotalCount)
			require.EqualValues(t, changed, reads.Load(), "the overlay reads the changed books")
			sc.mu.Lock()
			asked := sc.invalGen
			sc.mu.Unlock()
			if changed > overlayRebuildThreshold {
				require.EqualValues(t, 1, asked, "past the threshold the request asks for a rebuild")
			} else {
				require.Zero(t, asked, "at the threshold no rebuild is asked for")
			}
		})
	}
}

// TestReviewQuery_PageCarriesGeneration: every page and ids response names
// the generations it was evaluated at, and is never cached by a client.
func TestReviewQuery_PageCarriesGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	want := fmt.Sprintf("%d.%d", store.MetadataCacheGeneration(), store.LibraryGeneration().Value())
	page, hdr, _ := servePage(t, h, "limit=2")
	require.Equal(t, want, page.Data.Generation)
	require.Equal(t, "no-store", hdr.Get("Cache-Control"))
	require.Len(t, page.Data.Results, 2)
	require.NotNil(t, page.Data.Facets)
	ids, hdr, _ := servePage(t, h, "ids=all")
	require.Equal(t, want, ids.Data.Generation)
	require.Equal(t, "no-store", hdr.Get("Cache-Control"))
	// Page rows are index rows: the evidence fields come from the ids= detail fetch.
	for _, r := range page.Data.Results {
		require.NotNil(t, r.Candidate)
		require.Empty(t, r.Candidate.Description)
		require.Nil(t, r.Candidate.ScoreBreakdown)
		require.NotEmpty(t, r.CandidateHash)
	}
}

// TestReviewQuery_BadFieldIs400: the Title box refuses what the lane refuses,
// with the lane's words.
func TestReviewQuery_BadFieldIs400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	for q, want := range map[string]string{
		"q=author:smith":         "author: only title: filters apply here",
		"q=title:a+extra":        `"extra": mix of title: tokens and bare text — write every part as title:…`,
		"q=title:/a(b/":          "title:/a(b/ — invalid regex",
		"q=/(?=x)/":              "no lookahead",
		"chip=nope":              "chip must be one of",
		"sort=author":            "sort must be",
		"limit=0":                "limit must be",
		"min_confidence=101":     "min_confidence must be",
		"hide_runtime=maybe":     "hide_runtime must be true or false",
		"ids=" + goldenBookID(1): "ids must be all",
		"bucket=elsewhere":       "bucket must be",
	} {
		code, body, hdr := serveReviewRaw(t, h, "view=page&"+q)
		require.Equal(t, http.StatusBadRequest, code, q)
		var e struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &e), q)
		require.Contains(t, e.Error, want, q)
		require.Equal(t, "no-store", hdr.Get("Cache-Control"))
	}
	// Words the lane reads as free text are not field tokens.
	code, body, _ := serveReviewRaw(t, h, "view=page&q=NOT+foo:bar")
	require.Equal(t, http.StatusOK, code, string(body))
}

// reviewQueryError400 serves q and returns the 400's error text and how long
// the request took.
func reviewQueryError400(t *testing.T, h *MetadataCacheHandler, q string) (string, time.Duration) {
	t.Helper()
	start := time.Now()
	code, body, _ := serveReviewRaw(t, h, "view=page&q="+url.QueryEscape(q))
	took := time.Since(start)
	require.Equal(t, http.StatusBadRequest, code, "%.40s: %s", q, body)
	var e struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &e))
	return e.Error, took
}

// TestReviewQuery_LimitsRefuseQuickly: the three patterns measured at 1.3 s,
// 23.5 s and 2m10s over 40,000 titles before the limits are refused at parse
// time, in milliseconds, with a 400 that says what to change; none of them
// reaches an evaluation.
func TestReviewQuery_LimitsRefuseQuickly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	for _, tc := range []struct{ name, q, want string }{
		{"(.*){1000}", `/(.*){1000}/`, "too complex to run over the library"},
		{"(?:.?){1000}zzz", `/(?:.?){1000}zzz/`, "too complex to run over the library"},
		{"30 KB pattern", "/" + strings.Repeat("(a|b)", 6000) + "/", "q is 30002 bytes long; the limit is 1024"},
		{"300-byte value", strings.Repeat("x", 300), "the limit is 256"},
		{"1033-byte q of short tokens", strings.TrimSpace(strings.Repeat("title:abcd ", 94)), "q is 1033 bytes long; the limit is 1024"},
	} {
		msg, took := reviewQueryError400(t, h, tc.q)
		t.Logf("%s: refused in %s: %s", tc.name, took, msg)
		require.Contains(t, msg, tc.want, tc.name)
		require.Less(t, took, 100*time.Millisecond, tc.name)
		require.Less(t, len(msg), 400, "a refused value is not echoed in full")
	}
	require.Zero(t, h.reviewQuery.evaluations.Load(), "no refused pattern was evaluated")
	// Just under the q cap, a q of short tokens is still a query.
	code, body, _ := serveReviewRaw(t, h, "view=page&q="+url.QueryEscape(strings.TrimSpace(strings.Repeat("title:abc ", 102))))
	require.Equal(t, http.StatusOK, code, string(body))
}

// TestReviewQuery_DeadlineStopsEvaluation: a pattern inside the size limits
// that is still slow over the whole set stops at the deadline with an error,
// never a partial list.
func TestReviewQuery_DeadlineStopsEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 40,000-row snapshot")
	}
	snap := syntheticReviewSnapshot(40000, true)
	set, err := overlayLiveBooks(snap, nil, nil, changedIDs())
	require.NoError(t, err)
	base := newReviewQueryBase(reviewBaseKey{snap: snap}, set, time.Now())
	// 65 instructions: allowed, and the slowest shape per instruction
	// (nested optional repetition) -- about 250 ms over these titles.
	q := mustReviewQuery(t, map[string]string{"q": `/(?:.?){30}zzz/`})
	for _, params := range []map[string]string{
		{"q": `/(?:.?){30}zzz/`},
		{"q": `/(?:.?){30}zzz/`, "chip": "total"},
		{"q": `/(?:.?){30}zzz/`, "chip": "stale"},
	} {
		q = mustReviewQuery(t, params)
		const budget = 20 * time.Millisecond
		start := time.Now()
		l, err := evaluateReviewQuery(base, q, start.Add(budget))
		took := time.Since(start)
		t.Logf("%v: stopped after %s (deadline %s)", params, took, budget)
		require.Nil(t, l, "a stopped evaluation returns no partial list")
		var refused *reviewQueryError
		require.ErrorAs(t, err, &refused)
		require.Contains(t, refused.Error(), "took longer than")
		require.Less(t, took, budget+50*time.Millisecond, "the clock is read often enough to stop near the deadline")
	}
	// Given time, the same pattern completes: the refusal was the clock, not
	// the pattern. (Whether it fits reviewQueryEvalDeadline is a timing
	// claim, measured in querygrammar.MaxPatternInst's comment, not asserted
	// here: -race slows this pass past it.)
	l, err := evaluateReviewQuery(base, q, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, l.refs)
}

// TestReviewQuery_DeadlineIs400AndNotCached: through the handler, a stopped
// evaluation is a 400 every waiter sees, and it is not stored, so the next
// request evaluates again rather than reading a refusal (or a partial list)
// from the LRU.
func TestReviewQuery_DeadlineIs400AndNotCached(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	saved := reviewQueryEvalDeadline
	reviewQueryEvalDeadline = -time.Second // every evaluation is already past it
	t.Cleanup(func() { reviewQueryEvalDeadline = saved })

	for range 2 {
		msg, _ := reviewQueryError400(t, h, "title")
		require.Contains(t, msg, "took longer than")
	}
	require.EqualValues(t, 2, h.reviewQuery.evaluations.Load(), "a refusal is never served from the LRU")

	reviewQueryEvalDeadline = saved
	page, _, _ := servePage(t, h, "q=title")
	require.NotZero(t, page.Data.TotalCount)
}

// TestReviewQuery_ConcurrentIdenticalRequestsShareOneEvaluation: identical
// requests arriving together run one evaluation (singleflight, then the LRU),
// and different filters in parallel each run their own over one shared base.
func TestReviewQuery_ConcurrentIdenticalRequestsShareOneEvaluation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := goldenReviewStore(t)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	servePage(t, h, "chip=total") // the cold start
	before := h.reviewQuery.evaluations.Load()

	var wg sync.WaitGroup
	totals := make([]int, 16)
	for i := range totals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := "hide_runtime=true&q=title"
			if i%2 == 1 {
				q = "sort=confidence&ids=all"
			}
			r, _, _ := servePage(t, h, q)
			totals[i] = r.Data.TotalCount + r.Data.Total
		}()
	}
	wg.Wait()
	require.EqualValues(t, 2, h.reviewQuery.evaluations.Load()-before)
	require.EqualValues(t, 1, h.reviewQuery.bases.Load())
	for i := 2; i < len(totals); i++ {
		require.Equal(t, totals[i%2], totals[i])
	}
}
