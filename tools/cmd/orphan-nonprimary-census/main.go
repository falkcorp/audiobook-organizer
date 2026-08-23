// file: tools/cmd/orphan-nonprimary-census/main.go
// version: 1.1.0
// guid: e02ce63c-3475-470d-a2c4-a8766769d3c6
// last-edited: 2026-08-23
//
// orphan-nonprimary-census is a READ-ONLY diagnostic CLI that counts books in
// an anomalous population: no version group (VersionGroupID nil/empty) AND
// explicitly marked non-primary (IsPrimaryVersion != nil && *IsPrimaryVersion
// == false). This is distinct from the much larger population of books with
// IsPrimaryVersion == nil, which the rest of the codebase treats as primary
// (visible) — see internal/audiobooks/service_filtering.go's
// `eff := s.IsPrimaryVersion == nil || *s.IsPrimaryVersion` convention.
// Conflating the two populations would inflate this census roughly 24x.
// Full-library page-through on production 2026-08-23 (56,727 rows):
//
//	flag  has-VG  count
//	true    yes   37,613
//	false   yes   14,870
//	false   no       116  <- the target anomaly
//	nil     no     2,776
//	nil     yes         0
//	true    no      1,352
//
// Against the same cross-tab taken 2026-08-14 (63,839 rows) the anomaly grew
// 41 -> 116 while the library shrank by 7,112. Two structural claims recorded
// then: "every nil book is a groupless singleton" still holds (nil/yes = 0),
// but "true + no VG = 0" has BROKEN — 1,352 books now sit there. Re-measure
// rather than trusting either figure; this population is not stable.
//
// No DB writes. No mutating API calls. Output is a CSV report (book ID,
// title, created_at, updated_at, version_group_id, is_primary_version) plus a
// stderr summary, for correlating the anomalous rows' timestamps against
// known import/dedup/merge job runs.
//
// Modeled on tools/cmd/reconcile-paths's read-only CLI structure: flag-driven,
// paginated API fetch, CSV output.
//
// Usage:
//
//	orphan-nonprimary-census [-api URL] [-key KEY] [-out FILE] [-limit N] [-page-size N] [-page-delay-ms N] [-min-expected N] [-verbose]
package main

import (
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ----- API response types (mirrors internal/database.BookSummary) -----

type book struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	CreatedAt        *time.Time `json:"created_at,omitempty"`
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
	IsPrimaryVersion *bool      `json:"is_primary_version,omitempty"`
	VersionGroupID   *string    `json:"version_group_id,omitempty"`
}

type listData struct {
	Items  []book `json:"items"`
	Count  int    `json:"count"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// listResponse handles both wrapped {"data": {...}} and unwrapped shapes.
type listResponse struct {
	Data *listData `json:"data"`
	// Flat fields (fallback if API returns top-level directly)
	Items  []book `json:"items"`
	Count  int    `json:"count"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// ----- census predicate -----

// isOrphanNonPrimary reports whether b is in the anomalous population this
// tool censuses: no version group, AND explicitly marked non-primary.
//
// A nil IsPrimaryVersion does NOT match. internal/audiobooks/service_filtering.go
// treats a nil flag as primary/visible (`eff := s.IsPrimaryVersion == nil ||
// *s.IsPrimaryVersion`), so an ungrouped book with no stored flag is not
// evidence of this anomaly — it is the much larger, already-understood
// "no flag ever set" population (2,776 books measured in production
// 2026-08-23). Only an explicit false, set by something, is the target
// population.
//
// database.Book/BookSummary tag both fields `omitempty`, so a nil
// VersionGroupID or IsPrimaryVersion is indistinguishable from an absent key
// if read via raw JSON — this function reads the typed *string/*bool
// directly instead, so that distinction is preserved.
func isOrphanNonPrimary(b book) bool {
	if b.VersionGroupID != nil && strings.TrimSpace(*b.VersionGroupID) != "" {
		return false // has a version group — not an orphan
	}
	if b.IsPrimaryVersion == nil {
		return false // no stored flag — treated as primary elsewhere, not the target population
	}
	return !*b.IsPrimaryVersion // true only when explicitly false
}

// censusMatches returns the subset of books matching isOrphanNonPrimary.
func censusMatches(books []book) []book {
	var out []book
	for _, b := range books {
		if isOrphanNonPrimary(b) {
			out = append(out, b)
		}
	}
	return out
}

// ----- positive control -----

// errTooFewExamined is returned by checkPositiveControl when the scan
// examined suspiciously few books to trust its own "N orphans found" answer.
// A scan that silently examined nothing (a broken URL, an empty page, an
// auth failure returning an empty body) looks identical to a clean library
// reporting zero orphans unless something checks the denominator too.
type errTooFewExamined struct {
	examined, minExpected int
}

func (e *errTooFewExamined) Error() string {
	return fmt.Sprintf("examined only %d books, below -min-expected=%d; refusing to trust "+
		"the result — this usually means the fetch silently returned an empty or truncated "+
		"set rather than the real library (wrong -api URL, auth failure, or a paging bug), "+
		"not that the library is actually this small", e.examined, e.minExpected)
}

// checkPositiveControl fails loudly when examined is implausibly small,
// rather than letting a broken fetch masquerade as "0 orphans found".
// minExpected <= 0 disables the check (used only for deliberately narrow
// runs, e.g. -limit).
func checkPositiveControl(examined, minExpected int) error {
	if minExpected <= 0 {
		return nil
	}
	if examined < minExpected {
		return &errTooFewExamined{examined: examined, minExpected: minExpected}
	}
	return nil
}

// fieldPresenceCounts tallies, over a fetched set, how many books carried a
// non-nil value for each field the census predicate depends on. A count of 0
// examined books returning here is covered by checkPositiveControl instead.
type fieldPresenceCounts struct {
	examined         int
	withVersionGroup int // VersionGroupID != nil (any value, including "")
	withPrimaryFlag  int // IsPrimaryVersion != nil (any value)
}

func countFieldPresence(books []book) fieldPresenceCounts {
	c := fieldPresenceCounts{examined: len(books)}
	for _, b := range books {
		if b.VersionGroupID != nil {
			c.withVersionGroup++
		}
		if b.IsPrimaryVersion != nil {
			c.withPrimaryFlag++
		}
	}
	return c
}

// checkFieldPresence fails loudly when is_primary_version was nil on EVERY
// single examined book. That specific shape — 100% nil across a
// full-library-sized fetch — is the signature of a wire-format regression
// (the tri-state field silently stopped round-tripping, e.g. a struct change
// somewhere between the store and the API response collapsed *bool to a
// plain bool, or dropped the field's omitempty tag) rather than a fact about
// this library: production is known to carry a real mix of nil/true/false
// values for this field. A census built on a field that never survived the
// wire would report "0 orphans found" indistinguishably from a truthful
// clean scan, so this must fail instead of silently trusting it.
//
// VersionGroupID is NOT checked the same way: most books legitimately have
// no version group, so "0 books have one" is not on its own evidence of a
// wire problem the way "0 books have any is_primary_version value" is.
//
// KNOWN BLIND SPOT — this guard is one-sided. It catches the field
// DISAPPEARING (100% nil); it cannot catch the field COLLAPSING TO A
// CONSTANT. PR #2805 serializes the effective flag rather than the raw
// nullable, so a nil becomes an explicit `true` on the wire and
// withPrimaryFlag goes to 100% permanently, retiring this guard rather than
// tripping it.
//
// That does not move the census itself: the predicate keys on explicit
// FALSE, and #2805 changes only absent -> true. It does invalidate one
// downstream use. TODO.md's C111 entry nominates this census as the
// post-fix verification that "the expected end state is exactly two
// populations (true, false+VG)" — but once nils render as `true`, the API
// reports that end state whether or not the backfill ever ran. After #2805
// deploys, that specific verification has to read the store directly; the
// API can no longer distinguish a backfilled true from a nil.
func checkFieldPresence(c fieldPresenceCounts) error {
	if c.examined == 0 {
		return nil // checkPositiveControl's job
	}
	if c.withPrimaryFlag == 0 {
		return fmt.Errorf("is_primary_version was nil on all %d examined books; this is the "+
			"signature of a wire-format regression (the tri-state field stopped "+
			"round-tripping between the store and this tool), not a fact about the library — "+
			"refusing to trust a 0-orphan result built on it", c.examined)
	}
	return nil
}

// ----- main -----

func main() {
	// No baked-in default: this is a public repo and a hardcoded internal address is
	// exactly the kind of fleet detail that must not ship in it. Mirrors reconcile-paths.
	apiURL := flag.String("api", os.Getenv("AUDIOBOOK_API_URL"), "API base URL (or set AUDIOBOOK_API_URL env)")
	outFile := flag.String("out", "/tmp/orphan_nonprimary_census.csv", "Output CSV path (use '-' for stdout)")
	limit := flag.Int("limit", 0, "Max books to inspect (0 = all)")
	apiKey := flag.String("key", "", "API key (or set AUDIOBOOK_API_KEY env)")
	pageSize := flag.Int("page-size", 200, "Pagination page size")
	pageDelayMs := flag.Int("page-delay-ms", 100, "Delay in milliseconds between page fetches (0 to disable)")
	minExpected := flag.Int("min-expected", 1000, "Fail loudly if fewer than this many books were examined (0 disables; a positive-control guard against a silently empty/broken fetch masquerading as a clean 0-orphan result); ignored when -limit is set")
	verbose := flag.Bool("verbose", false, "Verbose progress logging")
	stdout := flag.Bool("stdout", false, "Write CSV to stdout instead of -out file")
	flag.Parse()

	// Resolve API key.
	key := *apiKey
	if key == "" {
		key = os.Getenv("AUDIOBOOK_API_KEY")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "orphan-nonprimary-census: API key required: use -key flag or AUDIOBOOK_API_KEY env var")
		os.Exit(1)
	}
	if *apiURL == "" {
		fmt.Fprintln(os.Stderr, "orphan-nonprimary-census: API URL required: use -api flag or AUDIOBOOK_API_URL env var")
		os.Exit(1)
	}

	// Build HTTP client (TLS skip for self-signed cert, matching reconcile-paths).
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	client := &http.Client{Transport: transport}

	// Fetch the FULL, unfiltered book list. This deliberately does not rely on
	// the server's own ?is_primary_version=false query filter: this tool
	// exists to independently verify that population, not to re-report
	// whatever the API's own (already-tested-elsewhere) filter says.
	books, dupes, err := fetchAllBooks(client, *apiURL, key, *pageSize, *limit, *pageDelayMs, *verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orphan-nonprimary-census: fetch books: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Fetched %d distinct books from API\n", len(books))

	// A duplicate is not a harmless double-count. Offset paging over a live
	// sorted list shifts rows across page boundaries, and a row that moves
	// backwards is served twice while some other row is skipped entirely. So
	// duplicates > 0 means rows were probably MISSED, and the anomaly count
	// below is a lower bound rather than a census. Not fatal — re-running
	// against a quiet library is the fix — but it must never be silent.
	if dupes > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: the server returned %d duplicate book rows during paging. "+
			"The library changed underneath this scan, so rows were likely MISSED too: treat every "+
			"number below as a LOWER BOUND, not a census. Re-run when the library is idle.\n", dupes)
	}

	presence := countFieldPresence(books)
	fmt.Fprintf(os.Stderr, "Field presence: %d/%d books have a version_group_id, %d/%d have an is_primary_version flag\n",
		presence.withVersionGroup, presence.examined, presence.withPrimaryFlag, presence.examined)

	// Positive control: a fetch that silently returned nothing (or almost
	// nothing), or returned rows whose tri-state fields never survived the
	// wire, must not be allowed to report "0 orphans found" as if the
	// library were actually clean. Both guards are skipped when the caller
	// explicitly narrowed the run with -limit.
	if *limit == 0 {
		if pcErr := checkPositiveControl(len(books), *minExpected); pcErr != nil {
			fmt.Fprintf(os.Stderr, "orphan-nonprimary-census: %v\n", pcErr)
			os.Exit(1)
		}
		if fpErr := checkFieldPresence(presence); fpErr != nil {
			fmt.Fprintf(os.Stderr, "orphan-nonprimary-census: %v\n", fpErr)
			os.Exit(1)
		}
	}

	matches := censusMatches(books)
	fmt.Fprintf(os.Stderr, "%d books are ungrouped AND explicitly non-primary (the target anomaly)\n", len(matches))

	// Write CSV.
	var w io.Writer
	if *stdout || *outFile == "-" {
		w = os.Stdout
	} else {
		f, err := os.Create(*outFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "orphan-nonprimary-census: create output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
		fmt.Fprintf(os.Stderr, "Writing CSV to %s\n", *outFile)
	}
	if err := writeCSV(w, matches); err != nil {
		fmt.Fprintf(os.Stderr, "orphan-nonprimary-census: write CSV: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\n=== SUMMARY ===\n")
	fmt.Fprintf(os.Stderr, "Total books inspected:                    %d\n", len(books))
	fmt.Fprintf(os.Stderr, "Duplicate rows served during paging:      %d\n", dupes)
	fmt.Fprintf(os.Stderr, "Books with a version_group_id set:        %d\n", presence.withVersionGroup)
	fmt.Fprintf(os.Stderr, "Books with an is_primary_version flag:    %d\n", presence.withPrimaryFlag)
	fmt.Fprintf(os.Stderr, "Ungrouped AND explicitly non-primary:     %d\n", len(matches))
}

// ----- API pagination -----

// fetchAllBooks pages the whole library and returns the de-duplicated books
// plus the number of duplicate rows the server handed back.
//
// Duplicates are counted rather than tolerated because this is offset-based
// paging over a live, sorted collection: a concurrent insert or a re-sort
// between two requests shifts rows across the page boundary, so the same book
// can arrive twice while another is never returned at all. That inflates the
// row count while silently dropping rows, which is exactly the failure a
// census must not report as a clean scan. Measured 0 duplicates over 56,727
// rows on production 2026-08-23, but that is a lucky quiet library, not a
// property of the algorithm.
func fetchAllBooks(client *http.Client, apiURL, key string, pageSize, limit, pageDelayMs int, verbose bool) ([]book, int, error) {
	var all []book
	seen := make(map[string]struct{})
	duplicates := 0
	offset := 0
	for {
		// `limit`, not `page_size`: the handler reads `limit` and ignores
		// `page_size` entirely. Measured against production 2026-08-23 —
		// `page_size=5` returned 50 items (the server default) while
		// `limit=5` returned 5. Sending `page_size` made the -page-size flag
		// inert and pinned every run to 50 rows/request.
		//
		// `show_quarantined=true` so the census sees the whole library. The
		// default path sets ExcludeQuarantined and hides quarantined rows; a
		// quarantined book is still a real row that can carry the anomaly.
		// Measured 2026-08-23 this cost nothing (both quarantined books were
		// primary), but that is a property of today's data, not a guarantee.
		url := fmt.Sprintf("%s/api/v1/audiobooks?limit=%d&offset=%d&show_quarantined=true", apiURL, pageSize, offset)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+key)

		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("GET %s: %w", url, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, 0, fmt.Errorf("API returned %d: %s", resp.StatusCode, body)
		}

		var lr listResponse
		if err := json.Unmarshal(body, &lr); err != nil {
			return nil, 0, fmt.Errorf("decode response: %w", err)
		}
		// Unwrap data envelope if present.
		items := lr.Items
		count := lr.Count
		if lr.Data != nil {
			items = lr.Data.Items
			count = lr.Data.Count
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "Page offset=%d: got %d items (total count=%d)\n", offset, len(items), count)
		}
		for _, b := range items {
			if _, dup := seen[b.ID]; dup {
				duplicates++
				continue
			}
			seen[b.ID] = struct{}{}
			all = append(all, b)
		}

		if limit > 0 && len(all) >= limit {
			all = all[:limit]
			break
		}
		// Terminate on an EMPTY PAGE ONLY. Deliberately NOT on
		// `len(all) >= count`.
		//
		// `count` is not the size of this result set. When no other filter is
		// set, buildAudiobookListResponse's `hasFilters` test is false and the
		// count comes from CountAudiobooks -> CountPrimaryBooks(), which
		// counts PRIMARY, NON-DELETED books only, while the item stream is not
		// primary-filtered at all. Measured against production 2026-08-23:
		// count reported 41,741 while the stream held 56,727, and the first
		// page of 250 was 240 non-primary books. Breaking on `count` here
		// would have silently truncated this census by 14,986 books — and
		// -min-expected, which only guards the low end, would have passed it.
		//
		// An empty page is the only honest end-of-stream signal available.
		if len(items) == 0 {
			break
		}
		offset += len(items)

		// Rate-limit subsequent page fetches to avoid overwhelming the server.
		if pageDelayMs > 0 {
			time.Sleep(time.Duration(pageDelayMs) * time.Millisecond)
		}
	}
	return all, duplicates, nil
}

// ----- CSV output -----

func writeCSV(w io.Writer, matches []book) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"book_id", "title", "created_at", "updated_at", "version_group_id", "is_primary_version",
	}); err != nil {
		return err
	}
	for _, b := range matches {
		if err := cw.Write([]string{
			b.ID,
			b.Title,
			formatTimePtr(b.CreatedAt),
			formatTimePtr(b.UpdatedAt),
			formatStringPtr(b.VersionGroupID),
			formatBoolPtr(b.IsPrimaryVersion),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func formatStringPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func formatBoolPtr(b *bool) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(*b)
}
