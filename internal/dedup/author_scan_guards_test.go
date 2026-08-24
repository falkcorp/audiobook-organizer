// file: internal/dedup/author_scan_guards_test.go
// version: 1.1.0
// guid: 5c7e2b91-8f36-4a0d-b214-6e9d3a75f0c2
// last-edited: 2026-08-24

package dedup

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The tests in this file guard phase 3 THROUGH FindDuplicateAuthors, rather
// than through the prefilter's own unit tests. That distinction is the whole
// point of the file: jaroWinklerBelowThreshold is covered well in isolation,
// but a unit test cannot observe whether a real duplicate actually survives the
// production path, and the failures these guard against are all "a genuine
// duplicate is silently never offered for merging."

func guardBookCount(id int) int { return id%7 + 1 }

// groupsContain reports whether some group holds both names, in either role.
func groupsContain(groups []AuthorDedupGroup, a, b string) bool {
	for _, g := range groups {
		names := []string{g.Canonical.Name}
		for _, v := range g.Variants {
			names = append(names, v.Name)
		}
		var seenA, seenB bool
		for _, n := range names {
			if n == a {
				seenA = true
			}
			if n == b {
				seenB = true
			}
		}
		if seenA && seenB {
			return true
		}
	}
	return false
}

// TestFindDuplicateAuthorsKeepsNonASCIIDuplicate is the integration guard for
// the prefilter counting RUNES rather than bytes.
//
// Nothing in the normalization chain folds diacritics -- NormalizeAuthorName
// trims, collapses whitespace, strips a leading conjunction and expands
// initials; the bucket key is just strings.ToLower of the surname -- so a name
// like "Kowalskiüö" reaches the length screen with its accents intact, and the
// rune-vs-byte distinction is load-bearing on the production path.
//
// "Kowalski" vs "Kowalskiüö" is 8/10 runes (ratio 0.800, comfortably above the
// 0.75 bound, so it must be KEPT) but 8/12 bytes (ratio 0.667, below the bound,
// so byte counting would SKIP it). Their true Jaro-Winkler is 0.96, well over
// the 0.95 gate. So under a byte-counting regression this pair is a real
// duplicate that is silently discarded -- and on a library of European author
// names that shape is common, not exotic.
//
// The existing TestJaroWinklerBelowThresholdCountsRunesNotBytes cannot cover
// this: it uses a cross-script pair that can never match, so it can assert only
// that the filter declines to skip, never that a real group survives.
func TestFindDuplicateAuthorsKeepsNonASCIIDuplicate(t *testing.T) {
	authors := []database.Author{
		{ID: 1, Name: "John Kowalski"},
		{ID: 2, Name: "John Kowalskiüö"},
		{ID: 3, Name: "Maria Andersen"},
		{ID: 4, Name: "Maria Andersenéé"},
	}
	groups := FindDuplicateAuthors(authors, 0.85, guardBookCount)

	if !groupsContain(groups, "John Kowalski", "John Kowalskiüö") {
		t.Errorf("non-ASCII duplicate was not grouped: John Kowalski / John Kowalskiüö\n%s",
			serializeGroups(groups))
	}
	if !groupsContain(groups, "Maria Andersen", "Maria Andersenéé") {
		t.Errorf("non-ASCII duplicate was not grouped: Maria Andersen / Maria Andersenéé\n%s",
			serializeGroups(groups))
	}
}

// TestFindDuplicateAuthorsKeepsBoundaryRatioDuplicate pins a real duplicate
// sitting exactly ON the prefilter's bound, so that any tightening of the bound
// at all breaks it.
//
// "Smithe" vs "Smithers" is 6/8 runes -- a ratio of exactly 0.7500, which is
// exactly 5t-4 at t=0.95 -- and scores exactly 0.9500, exactly the gate. It is
// therefore the tightest pair the filter is allowed to keep.
//
// This matters because the golden corpus does NOT cover it. The closest true
// match in determinismCorpus is "anderson"/"andersonn" at ratio 0.889, so the
// golden stays green against any bound tightened anywhere below 0.889 -- a
// range that includes bounds which would discard real duplicates on the live
// library. This test closes that band.
func TestFindDuplicateAuthorsKeepsBoundaryRatioDuplicate(t *testing.T) {
	if got := jaroWinklerSimilarity("smithe", "smithers"); got < authorLastNameSimilarity {
		t.Fatalf("fixture no longer sits on the gate: JW(smithe, smithers) = %v, want >= %v",
			got, authorLastNameSimilarity)
	}
	if jaroWinklerBelowThreshold("smithe", "smithers", authorLastNameSimilarity) {
		t.Fatal("prefilter skips a pair that scores exactly at the gate; the bound is too tight")
	}

	authors := []database.Author{
		{ID: 1, Name: "Anna Smithe"},
		{ID: 2, Name: "Anna Smithers"},
	}
	groups := FindDuplicateAuthors(authors, 0.85, guardBookCount)
	if !groupsContain(groups, "Anna Smithe", "Anna Smithers") {
		t.Errorf("boundary-ratio duplicate was not grouped: Anna Smithe / Anna Smithers\n%s",
			serializeGroups(groups))
	}
}

// TestFindDuplicateAuthorsGoldenAcrossShardCounts runs the golden corpus at
// several fixed worker counts.
//
// Without this, every test observes exactly one shard configuration: whatever
// runtime.NumCPU() reports on the machine running it. On a single-core CI
// runner the scan collapses to one worker and the parallel path is never
// exercised at all, while the whole suite stays green -- so the concurrency
// this PR introduced would be untested precisely where it is least observed.
//
// 64 workers against 27 surnames also pins the over-subscription clamp, and 1
// worker gives the serial-vs-parallel equivalence oracle that the one-time
// cross-commit check could not leave behind as a test.
func TestFindDuplicateAuthorsGoldenAcrossShardCounts(t *testing.T) {
	original := scanWorkerCount
	t.Cleanup(func() { scanWorkerCount = original })

	for _, workers := range []int{1, 2, 3, 8, 64} {
		scanWorkerCount = workers
		got := serializeGroups(FindDuplicateAuthors(determinismCorpus(), 0.85, guardBookCount))
		if got != wantGoldenGroups {
			gotLines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			wantLines := strings.Split(strings.TrimRight(wantGoldenGroups, "\n"), "\n")
			t.Errorf("workers=%d changed the grouping: got %d groups, want %d",
				workers, len(gotLines), len(wantLines))
			for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
				if gotLines[i] != wantLines[i] {
					t.Errorf("workers=%d line %d:\n  got:  %s\n  want: %s",
						workers, i+1, gotLines[i], wantLines[i])
					break
				}
			}
		}
	}
}

// denseScanCorpus builds an author set in which almost every surname has a
// similar partner, so that almost every outer index of the phase-3 scan carries
// at least one match.
//
// This exists because the golden corpus does not have that property, and the
// gap is only visible under mutation. determinismCorpus deliberately includes
// eight mutually-dissimilar padding authors, and its families are large, so
// matches are concentrated on a minority of indices: mutating the scan to drop
// every index where li%7 == 3 leaves the ENTIRE suite green, because those
// particular indices happen to carry no matches. (Dropping li%7 == 0 fails four
// tests, so the golden is not blind in general -- only sparse.)
//
// Pairs of surnames, no padding, means a dropped index is a dropped group.
func denseScanCorpus() []database.Author {
	// Every pair below was measured to score >= 0.95, so all 14 really do
	// produce groups. Short surnames were deliberately avoided: Jaro-Winkler is
	// length-sensitive, so the same single-character substitution that clears
	// the gate in "gundersen"/"gunderson" (0.9556) falls short in
	// "olsen"/"olson" (0.9067). A fixture built from short names would silently
	// yield far fewer groups than it appears to.
	pairs := [][2]string{
		{"Kristiansen", "Kristianson"}, {"Andreasen", "Andreason"},
		{"Mortensen", "Mortenson"}, {"Dahlberg", "Dahlbergh"},
		{"Thorvaldsen", "Thorvaldson"}, {"Steffensen", "Steffenson"},
		{"Gundersen", "Gunderson"}, {"Halvorsen", "Halvorson"},
		{"Ivarsson", "Ivarson"}, {"Jakobsen", "Jakobson"},
		{"Pedersen", "Pederson"}, {"Henriksen", "Henrikson"},
		{"Johannsen", "Johannson"}, {"Rasmussen", "Rasmusson"},
	}
	var authors []database.Author
	id := 1
	for _, p := range pairs {
		for _, first := range []string{"John", "Maria", "Erik"} {
			authors = append(authors, database.Author{ID: id, Name: first + " " + p[0]})
			id++
			authors = append(authors, database.Author{ID: id, Name: first + " " + p[1]})
			id++
		}
	}
	return authors
}

// TestFindDuplicateAuthorsDenseScanIsShardStable is the dropped-pair guard.
//
// Every surname here has a partner, so a scan that silently loses ANY outer
// index loses groups. Rather than pinning a second golden constant, this
// compares the single-worker result against every other shard count: workers=1
// is the serial reference, and any divergence means the parallel path lost or
// duplicated work. A drop that affects all shard counts identically is caught
// by the group-count floor instead.
func TestFindDuplicateAuthorsDenseScanIsShardStable(t *testing.T) {
	original := scanWorkerCount
	t.Cleanup(func() { scanWorkerCount = original })

	authors := denseScanCorpus()

	scanWorkerCount = 1
	reference := serializeGroups(FindDuplicateAuthors(authors, 0.85, guardBookCount))
	groups := strings.Count(strings.TrimSpace(reference), "\n") + 1

	// 14 surname pairs x 3 first names = 42 groups if nothing is lost. Asserting
	// the floor keeps this test from passing vacuously if the corpus is edited
	// into one that no longer matches, and catches a drop that hits every shard
	// count equally.
	if groups < 42 {
		t.Fatalf("dense corpus produced only %d groups, expected 42; the scan is "+
			"losing pairs or the corpus no longer matches:\n%s", groups, reference)
	}

	for _, workers := range []int{2, 3, 5, 7, 16, 64} {
		scanWorkerCount = workers
		got := serializeGroups(FindDuplicateAuthors(authors, 0.85, guardBookCount))
		if got != reference {
			t.Errorf("workers=%d diverged from the serial reference (workers=1)."+
				"\n--- workers=1 ---\n%s\n--- workers=%d ---\n%s",
				workers, reference, workers, got)
		}
	}
	t.Logf("%d groups identical across 7 shard counts", groups)
}

// TestFindDuplicateAuthorsHandlesDegenerateInputs covers the cases where the
// worker count clamps to zero.
//
// These pass today. The point is regression insurance for a specific future
// edit: someone "simplifying" the clamp to max(1, ...) or replacing the atomic
// counter with a static contiguous range split would produce workers=0 over a
// non-empty range, or workers=1 over an empty one, and hang on scanWG.Wait().
//
// The third case is the one that is easy to miss, because the author list is
// NOT empty -- every author is discarded by the name filters, so the buckets
// map ends up empty while len(authors) is 3.
func TestFindDuplicateAuthorsHandlesDegenerateInputs(t *testing.T) {
	cases := []struct {
		name    string
		authors []database.Author
	}{
		{"nil slice", nil},
		{"empty slice", []database.Author{}},
		{"single author", []database.Author{{ID: 1, Name: "Ursula Le Guin"}}},
		{"two authors", []database.Author{
			{ID: 1, Name: "Ursula Le Guin"},
			{ID: 2, Name: "Terry Pratchett"},
		}},
		{"non-empty but every author filtered out", []database.Author{
			{ID: 1, Name: "Penguin Random House"},
			{ID: 2, Name: "Neal Stephenson - Snow Crash"},
			{ID: 3, Name: "A, B, C, D"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A hang here is the failure mode being guarded against; Go's test
			// timeout turns that into a readable panic with all goroutines.
			groups := FindDuplicateAuthors(tc.authors, 0.85, guardBookCount)
			if len(groups) != 0 {
				t.Errorf("expected no duplicate groups, got %d:\n%s",
					len(groups), serializeGroups(groups))
			}
		})
	}
}
