// file: internal/server/handlers/abs/filter_plus_test.go
// version: 1.0.0
// guid: 2f9c6e04-7b1a-4d83-95e2-c8a0b3d71f56
// last-edited: 2026-09-25

package abs_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// TestItemsFilter_Base64PlusSurvivesTheAppsQueryEncoding pins the bug the AudioBooth
// decode proof found on 2026-09-25.
//
// AudioBooth builds ?filter=<group>.<base64 value> through URLComponents.queryItems,
// which leaves '+' LITERAL in the query (measured with swift 6.4). A query parser
// reads a literal '+' as a space, so any value whose base64 contains '+' reached
// absFilterGroup with a space in it, failed every base64 alphabet, and served an
// EMPTY page. "Seán O’Brien" is such a name: picking him from the narrator filter
// showed "no books".
//
// The %2B form is what a Go client (url.Values) sends; it must keep working too.
func TestItemsFilter_Base64PlusSurvivesTheAppsQueryEncoding(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte(audioboothNarratorPlus))
	if !strings.Contains(enc, "+") {
		t.Fatalf("fixture name %q no longer encodes with a '+' (%s); pick another", audioboothNarratorPlus, enc)
	}
	seed := seedAudioBoothLibrary(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	for name, token := range map[string]string{
		"literal plus (AudioBooth / URLComponents)": enc,
		"percent-encoded plus (url.Values)":         strings.ReplaceAll(enc, "+", "%2B"),
	} {
		t.Run(name, func(t *testing.T) {
			_, body := h.do(t, request{
				method:  http.MethodGet,
				path:    "/api/libraries/" + h.libraryID() + "/items?minified=1&limit=100&page=0&filter=narrators." + token,
				headers: bearer(tok),
			})
			results, _ := body["results"].([]any)
			if len(results) != 1 {
				t.Fatalf("narrator filter for %q returned %d books, want 1 (the book he narrates)",
					audioboothNarratorPlus, len(results))
			}
			if got := body["total"]; got != float64(1) {
				t.Fatalf("total = %v, want 1", got)
			}
		})
	}
}
