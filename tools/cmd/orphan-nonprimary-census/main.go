// file: tools/cmd/orphan-nonprimary-census/main.go
// version: 1.0.0
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
// Conflating the two populations would inflate this census roughly 140x
// (~5,731 nil-flagged books measured in production vs. a sampled ~41
// explicitly-false ones).
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
// "no flag ever set" population (~5,731 books in production). Only an
// explicit false, set by something, is the target population.
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
	books, err := fetchAllBooks(client, *apiURL, key, *pageSize, *limit, *pageDelayMs, *verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orphan-nonprimary-census: fetch books: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Fetched %d books from API\n", len(books))

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
	fmt.Fprintf(os.Stderr, "Books with a version_group_id set:        %d\n", presence.withVersionGroup)
	fmt.Fprintf(os.Stderr, "Books with an is_primary_version flag:    %d\n", presence.withPrimaryFlag)
	fmt.Fprintf(os.Stderr, "Ungrouped AND explicitly non-primary:     %d\n", len(matches))
}

// ----- API pagination -----

func fetchAllBooks(client *http.Client, apiURL, key string, pageSize, limit, pageDelayMs int, verbose bool) ([]book, error) {
	var all []book
	offset := 0
	for {
		url := fmt.Sprintf("%s/api/v1/audiobooks?page_size=%d&offset=%d", apiURL, pageSize, offset)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", url, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, body)
		}

		var lr listResponse
		if err := json.Unmarshal(body, &lr); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
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
		all = append(all, items...)

		if limit > 0 && len(all) >= limit {
			all = all[:limit]
			break
		}
		// Server may cap page size below requested value; trust count and stop only when empty or count reached.
		if len(items) == 0 || (count > 0 && len(all) >= count) {
			break
		}
		offset += len(items)

		// Rate-limit subsequent page fetches to avoid overwhelming the server.
		if pageDelayMs > 0 {
			time.Sleep(time.Duration(pageDelayMs) * time.Millisecond)
		}
	}
	return all, nil
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
