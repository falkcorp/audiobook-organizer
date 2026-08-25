// file: internal/server/handlers/abs/contributor_filters_test.go
// version: 1.2.0
// guid: 3f8c1d54-9a20-4e7b-b6d1-8c4a2f01e9b7
// last-edited: 2026-08-25

package abs_test

import (
	"errors"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"
)

// seedContributors gives the fixture library enough shape to tell the sources and
// the orderings APART. The stock oracle fixture has two authors with one book
// each and no narrators, which cannot distinguish:
//
//   - name order from numBooks order (all counts equal),
//   - the contributor index from the raw store rows (no author lacks a book),
//   - a sorted response from an unsorted one (two elements are frequently
//     already in the asserted order by accident).
//
// Returns the visible author names in ascending name order.
func seedContributors(t *testing.T, w *writeHarness) []string {
	t.Helper()
	// Two books, so an author can hold a count no other author holds.
	w.seed.lib.addAuthor(3, "Zed Author", w.seed.singleID, w.seed.multiID)
	// 🔴 THE WHOLE POINT OF #6: an author row with NO visible book. In production
	// 4,975 of 12,854 authors (38.7%) are in this state; every one of them was
	// offered in the filter dropdown and returned an empty shelf when picked.
	w.seed.lib.addAuthor(99, "Ghost Author")

	w.seed.lib.attachNarrators(w.seed.singleID, "Real Narrator")
	w.seed.lib.addOrphanNarrators("Orphan Narrator")

	return []string{"Homer", "Zed Author", "transl. Samuel Butler Homer"}
}

func authorNames(t *testing.T, body map[string]any, key string) []string {
	t.Helper()
	var out []string
	for _, entry := range requireArray(t, body, key) {
		m, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("%s element is %T, want object", key, entry)
		}
		name, _ := m["name"].(string)
		out = append(out, name)
	}
	return out
}

func stringList(t *testing.T, body map[string]any, key string) []string {
	t.Helper()
	var out []string
	for _, entry := range requireArray(t, body, key) {
		s, ok := entry.(string)
		if !ok {
			t.Fatalf("%s element is %T, want string", key, entry)
		}
		out = append(out, s)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 🔴 TestAuthors_SortAppliesToTheBareShapeToo is the half that is easy to miss.
// LibraryAuthors has two response shapes and only the paginated one ECHOES
// sort/desc back, so sorting inside that branch looks complete while
// …/authors?sort=numBooks&desc=1 with no limit/page stays in name order.
func TestAuthors_SortAppliesToTheBareShapeToo(t *testing.T) {
	w := newWriteHarness(t)
	byName := seedContributors(t, w)

	code, body, raw := w.req(t, http.MethodGet,
		"/api/libraries/"+w.libraryID()+"/authors?sort=numBooks&desc=1", nil)
	if code != http.StatusOK {
		t.Fatalf("authors = %d %s", code, raw)
	}
	got := authorNames(t, body, "authors")
	if len(got) == 0 {
		t.Fatalf("no authors returned: %s", raw)
	}
	// Zed Author holds 2 books; every other visible author holds 1.
	if got[0] != "Zed Author" {
		t.Fatalf("?sort=numBooks&desc=1 (bare shape) returned %v; the most-prolific "+
			"author must lead, not name order %v", got, byName)
	}
}

// TestAuthors_SortDoesNotMutateTheSharedCache is the copy regression.
//
// 🔴 THE OBVIOUS VERSION OF THIS TEST IS BLIND, and mutation testing is the only
// reason that is known. Sorting ?desc=1 and then re-reading /authors with no
// sort params passes whether or not sortAuthors copies: an empty sort means
// "name", so the second request RE-SORTS ascending and repairs the corruption
// before it can be observed. The self-healing observer hides the defect.
//
// /filterdata is the honest observer. It reads idx.authors and does not sort,
// so an in-place sort in /authors reaches it intact — which is also the real
// user-facing consequence: sorting the Authors tab scrambles the filter menu
// for everyone, for the rest of the TTL.
func TestAuthors_SortDoesNotMutateTheSharedCache(t *testing.T) {
	w := newWriteHarness(t)
	byName := seedContributors(t, w)

	// Sorts by a key whose order differs from the index's name order.
	if code, _, raw := w.req(t, http.MethodGet,
		"/api/libraries/"+w.libraryID()+"/authors?sort=numBooks&desc=1", nil); code != http.StatusOK {
		t.Fatalf("authors = %d %s", code, raw)
	}

	_, body, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	got := authorNames(t, body, "authors")
	if !equalStrings(got, byName) {
		t.Fatalf("after ?sort=numBooks&desc=1 on /authors, the filter menu came back in "+
			"%v instead of %v — the sort mutated the shared cached slice every other "+
			"contributor endpoint reads: %s", got, byName, raw)
	}
}

// TestFilterData_AuthorsMatchTheAuthorsEndpoint encodes what PR #2512 built the
// contributor index FOR: the tile and the drill-down cannot drift. Asserting set
// EQUALITY is stronger than "no zero-book author appears" — it also fails if
// filterdata ever gains an author /authors does not have.
func TestFilterData_AuthorsMatchTheAuthorsEndpoint(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)

	_, authorsBody, _ := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/authors", nil)
	want := authorNames(t, authorsBody, "authors")

	code, fdBody, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	if code != http.StatusOK {
		t.Fatalf("filterdata = %d %s", code, raw)
	}
	got := authorNames(t, fdBody, "authors")

	sort.Strings(want)
	sort.Strings(got)
	if !equalStrings(got, want) {
		t.Fatalf("filterdata authors %v != /authors %v — picking the difference in the "+
			"filter dropdown returns an empty shelf", got, want)
	}
	for _, name := range got {
		if name == "Ghost Author" {
			t.Fatalf("filterdata still offers an author with no visible book: %v", got)
		}
	}
}

// TestFilterData_NarratorsExcludeThoseWithNoVisibleBook — the narrator list
// shrinks for the same reason the author list does, and from a different store
// call (ListNarrators), so it needs its own assertion.
func TestFilterData_NarratorsExcludeThoseWithNoVisibleBook(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)

	_, body, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	got := stringList(t, body, "narrators")

	var sawReal bool
	for _, n := range got {
		if n == "Orphan Narrator" {
			t.Fatalf("filterdata offers a narrator attached to no visible book: %v", got)
		}
		if n == "Real Narrator" {
			sawReal = true
		}
	}
	// Guards the opposite failure: an empty list would pass the check above
	// vacuously, which is exactly how the missing narrator id shipped.
	if !sawReal {
		t.Fatalf("filterdata dropped a narrator that IS on a visible book: %v — %s", got, raw)
	}
}

// TestFilterData_BuiltOncePerTTL — measured against production on 2026-08-25,
// this endpoint took 7.17s and then 6.57s on two CONSECUTIVE calls. It is on the
// library page-load path.
func TestFilterData_BuiltOncePerTTL(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)

	w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	before := w.seed.lib.genreCalls()
	for i := 0; i < 5; i++ {
		if code, _, raw := w.req(t, http.MethodGet,
			"/api/libraries/"+w.libraryID()+"/filterdata", nil); code != http.StatusOK {
			t.Fatalf("filterdata = %d %s", code, raw)
		}
	}
	if after := w.seed.lib.genreCalls(); after != before {
		t.Fatalf("filterdata rebuilt %d times across 5 repeat requests; each rebuild "+
			"walks the whole book keyspace twice", after-before)
	}
}

// 🔴 TestContributorIndex_ConcurrentColdCallersBuildOnce is the thundering herd.
//
// The TTL bounds how OFTEN the index is rebuilt, never how MANY rebuilds run at
// once, and those are different failures. N clients arriving on a cold cache
// each started their own full-library scan.
//
// 🔴 THIS WAS ALREADY LIVE BEFORE /filterdata WAS MOVED ONTO THE INDEX — eight
// call sites already reached it, including Personalized (the ABS home shelves)
// and LibrarySearch. An earlier version of this comment said /authors was the
// only trigger and needed a deliberate tab tap; that was wrong, and it made the
// bug sound rarer than it is.
func TestContributorIndex_ConcurrentColdCallersBuildOnce(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)

	release := w.seed.lib.holdContributorBuilds()
	defer release()

	const callers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/authors", nil)
		}()
	}
	close(start)

	// Wait for the first build to be counted and held on the gate.
	deadline := time.Now().Add(5 * time.Second)
	for w.seed.lib.contributorBuildCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	// Every build is blocked on the gate, so a second one that starts STAYS
	// counted — the number can only rise. That makes the wait one-sided: it can
	// add evidence of a herd but never erase it, so a short settle window is
	// enough and a slow machine biases toward the safe direction.
	settle := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(settle) {
		if n := w.seed.lib.contributorBuildCount(); n > 1 {
			release()
			wg.Wait()
			t.Fatalf("%d concurrent cold callers started %d full-library scans, want 1", callers, n)
		}
		time.Sleep(2 * time.Millisecond)
	}

	release()
	wg.Wait()
	if n := w.seed.lib.contributorBuildCount(); n != 1 {
		t.Fatalf("contributor index built %d times, want exactly 1", n)
	}
}

// 🔴 TestFilterData_DegradedDocumentIsNotCached is the caching blocker.
//
// buildFilterData degrades each source to an empty list rather than failing the
// request. That was the right cost when it was paid ONCE PER REQUEST. Adding a
// cache promoted it: stamping a degraded build pins one transient store error
// into a 200 for the whole TTL, and because filterDataFresh then short-circuits,
// the error is never logged again either.
//
// It also recreates the exact drift this endpoint was moved onto the contributor
// index to REMOVE: contributorsCached does not cache its error, so /authors
// recovers on its very next request while /filterdata would still be insisting
// the library has no authors.
func TestFilterData_DegradedDocumentIsNotCached(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)

	w.seed.lib.setListErr(errors.New("store unavailable"))
	_, degraded, _ := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	if got := authorNames(t, degraded, "authors"); len(got) != 0 {
		t.Fatalf("expected a degraded document while the store is failing, got %v", got)
	}

	// The store recovers. The very next request must rebuild, not serve the
	// pinned empty document for the rest of the TTL.
	w.seed.lib.setListErr(nil)
	_, body, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	got := authorNames(t, body, "authors")
	if len(got) == 0 {
		t.Fatalf("filterdata pinned a degraded document past the failure that caused it; "+
			"the store recovered and this request still returned no authors: %s", raw)
	}
}

// TestFilterData_DegradedServesTheLastGoodDocument — an empty list is not the
// ABSENCE of a filter, it is an affirmative claim that the library has no
// authors, and LoadedAt dates that claim as current. A stale document is at
// least true of some moment and says which.
func TestFilterData_DegradedServesTheLastGoodDocument(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)

	base := time.Now()
	w.handler.SetClock(func() time.Time { return base })

	_, good, _ := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	want := authorNames(t, good, "authors")
	if len(want) == 0 {
		t.Fatal("fixture produced no authors; the rest of this test cannot discriminate")
	}

	// Past the TTL, so the next request must rebuild — and the rebuild fails.
	w.handler.SetClock(func() time.Time { return base.Add(10 * time.Minute) })
	w.seed.lib.setListErr(errors.New("store unavailable"))

	_, body, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	got := authorNames(t, body, "authors")
	if !equalStrings(got, want) {
		t.Fatalf("a failed rebuild replaced the last good author list with %v, want %v — "+
			"the client is now told this library has no authors: %s", got, want, raw)
	}
}

// TestFilterData_ConcurrentColdCallersBuildOnce — /filterdata has its own
// singleflight group, and TestFilterData_BuiltOncePerTTL is sequential, so
// nothing else in this package ever runs filterDataCached from two goroutines.
func TestFilterData_ConcurrentColdCallersBuildOnce(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)

	release := w.seed.lib.holdFilterDataBuilds()
	defer release()

	const callers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
		}()
	}
	close(start)

	deadline := time.Now().Add(5 * time.Second)
	for w.seed.lib.genreCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	settle := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(settle) {
		if n := w.seed.lib.genreCalls(); n > 1 {
			release()
			wg.Wait()
			t.Fatalf("%d concurrent cold callers started %d filterdata builds, want 1", callers, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
	release()
	wg.Wait()
	if n := w.seed.lib.genreCalls(); n != 1 {
		t.Fatalf("filterdata document built %d times, want exactly 1", n)
	}
}

// 🔴 TestFilterData_ExpiresWithTheIndexItWasBuiltFrom — equal TTL LENGTHS do not
// align PHASES.
//
// The two caches are stamped independently, so a document stamped at its own
// build time can outlive the contributor index it was built from by up to a full
// TTL, and serve an author list /authors has already replaced. Stamping the
// document with the INDEX's build time is what actually makes them agree.
func TestFilterData_ExpiresWithTheIndexItWasBuiltFrom(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)

	base := time.Now()
	w.handler.SetClock(func() time.Time { return base })
	// Builds the contributor index at T=0.
	w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/authors", nil)

	// T=4m: the index is still fresh, so the document is built FROM the T=0
	// index. Stamped naively it would expire at T=9m; stamped with the index's
	// build time it expires at T=5m, with the index.
	w.handler.SetClock(func() time.Time { return base.Add(4 * time.Minute) })
	w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)
	before := w.seed.lib.genreCalls()

	// T=6m: the index has expired and /authors would rebuild it. The document
	// must expire too, or the two disagree for the next three minutes.
	w.handler.SetClock(func() time.Time { return base.Add(6 * time.Minute) })
	w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/filterdata", nil)

	if after := w.seed.lib.genreCalls(); after == before {
		t.Fatalf("the filter document outlived the contributor index it was built from: "+
			"the index expired at T=5m and this T=6m request still served the cached "+
			"document (genre scans %d -> %d)", before, after)
	}
}

// 🔴 TestAuthors_EchoReportsTheOrderActuallyApplied.
//
// sortBy/sortDesc are the server telling the client how the list is ordered.
// Echoing back a key we have no field for makes the response ASSERT an ordering
// it did not apply — the same defect, in the same handler, as the sort that was
// parsed and then ignored. Fixing the sort while leaving the echo raw would have
// left the lie intact for exactly the keys that still do not work.
func TestAuthors_EchoReportsTheOrderActuallyApplied(t *testing.T) {
	w := newWriteHarness(t)
	seedContributors(t, w)
	base := "/api/libraries/" + w.libraryID() + "/authors?limit=100&page=0"

	t.Run("an unsupported key is not echoed as applied", func(t *testing.T) {
		code, body, raw := w.req(t, http.MethodGet, base+"&sort=fileBirthtime&desc=1", nil)
		// Deliberately NOT a 400: these are third-party clients, and a response
		// they cannot use blanks the Authors tab. Serving the list in a stated
		// order is the smaller harm.
		if code != http.StatusOK {
			t.Fatalf("an unsupported sort must still serve the list, got %d %s", code, raw)
		}
		if got, _ := body["sortBy"].(string); got != "" {
			t.Fatalf("response claims it sorted by %q; nothing of the kind was applied: %s", got, raw)
		}
		if got, _ := body["sortDesc"].(bool); got {
			t.Fatalf("response claims descending order; the list is in the store default order: %s", raw)
		}
	})

	t.Run("a supported key is echoed unchanged", func(t *testing.T) {
		_, body, raw := w.req(t, http.MethodGet, base+"&sort=numBooks&desc=1", nil)
		if got, _ := body["sortBy"].(string); got != "numBooks" {
			t.Fatalf("sortBy = %q, want numBooks: %s", got, raw)
		}
		if got, _ := body["sortDesc"].(bool); !got {
			t.Fatalf("sortDesc = false, want true: %s", raw)
		}
		// Guards the lazy fix — reporting "" for everything would pass the
		// subtest above while telling every client nothing at all.
		names := authorNames(t, body, "results")
		if len(names) == 0 || names[0] != "Zed Author" {
			t.Fatalf("echo says numBooks-descending but the list is %v", names)
		}
	})
}
