// file: internal/server/handlers/abs/filterdata_decades_test.go
// version: 1.0.0
// guid: a4d3fc3f-08e0-4bfb-bf42-7081bb8235eb
// last-edited: 2026-09-11

package abs_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestFilterData_PublishedDecadesComeFromTheWholeLibrary is the SQ-05
// regression: /filterdata's publishedDecades used to be derived from
// GetAllBooksCore(5000, 0) — the first 5,000 rows in ULID (creation) order,
// every call, never the rest — so a decade whose only books were added after
// the first 5,000 was permanently absent from the client's filter dropdown,
// with nothing in the response or the logs to say so.
//
// The library here is 5,001 books: 5,000 from the 2000s, then ONE newest book
// from the 1880s. On the pre-fix code the decade list is ["2000"]; the fix
// must list "1880" as well. The fake's GetAllBooksCore honors its limit
// exactly the way the store does, so this fails for the real reason and not
// because the fake is generous.
func TestFilterData_PublishedDecadesComeFromTheWholeLibrary(t *testing.T) {
	const oldScanCap = 5000

	seed := seedOracleLibrary(t)
	intp := func(i int) *int { return &i }
	for i := range oldScanCap {
		seed.lib.addBook(&database.Book{
			ID:        fmt.Sprintf("01DECADEBULK%014d", i),
			Title:     fmt.Sprintf("Bulk %d", i),
			PrintYear: intp(2000 + i%10),
		}, nil, nil)
	}
	// The newest book, and the only one from its decade. AudiobookReleaseYear is
	// set on it as well, to pin that it wins over PrintYear when both exist.
	seed.lib.addBook(&database.Book{
		ID: "01DECADENEWESTBOOK00000001", Title: "Newest",
		PrintYear: intp(1995), AudiobookReleaseYear: intp(1884),
	}, nil, nil)

	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	w, _ := h.do(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/filterdata",
		headers: bearer(tok),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		PublishedDecades []string `json:"publishedDecades"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode filterdata: %v", err)
	}

	for _, decade := range []string{"1880", "2000"} {
		if !slices.Contains(resp.PublishedDecades, decade) {
			t.Errorf("publishedDecades = %v, missing %q: the facet was built from a bounded "+
				"page of the library, not the whole of it", resp.PublishedDecades, decade)
		}
	}
	// 1995 is the newest book's PrintYear; AudiobookReleaseYear (1884) must win.
	if slices.Contains(resp.PublishedDecades, "1990") {
		t.Errorf("publishedDecades = %v: PrintYear was used where AudiobookReleaseYear is set",
			resp.PublishedDecades)
	}
	if !slices.IsSorted(resp.PublishedDecades) {
		t.Errorf("publishedDecades = %v: not sorted", resp.PublishedDecades)
	}
}
