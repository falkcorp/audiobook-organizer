// file: internal/audiobooks/filter_field_conformance_test.go
// version: 1.0.0
// guid: 2d84f0b6-31ca-4e57-9a1f-6b70c8e2d415
// last-edited: 2026-08-14

package audiobooks

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// searchParserPath is the frontend's field list, read as a fixture. Relative to
// this package's directory.
const searchParserPath = "../../web/src/utils/searchParser.ts"

// uiSearchFieldsExemptFromBackendFilter lists names the search bar accepts that
// deliberately never reach the backend field-filter layer.
//
// One entry, and it needs the justification: "tag" is parsed out of the search
// string by the UI and sent as its own query parameter, not inside the filters
// JSON (see Library.tsx, which skips ff.field == "tag" when building the
// array). Tag filtering resolves through GetBooksByTag into a RestrictToIDs
// set, which is a different mechanism from substring-matching a book column.
//
// This list must stay tiny and each entry must say why. An exemption list is
// how a conformance test quietly stops conforming.
var uiSearchFieldsExemptFromBackendFilter = map[string]string{
	"tag": "handled client-side as a separate query parameter, resolved via GetBooksByTag",
}

// readUISearchFields extracts the SEARCH_FIELDS array from searchParser.ts.
func readUISearchFields(t *testing.T) []string {
	t.Helper()

	path, err := filepath.Abs(searchParserPath)
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	// Deliberately Fatal rather than Skip. A skip on a missing file turns this
	// into a test that silently stops running the day someone moves the
	// frontend, which is precisely the drift it exists to catch.
	require.NoErrorf(t, err, "cannot read the frontend search-field list at %s; if the file moved, "+
		"update searchParserPath rather than deleting this test", path)

	block := regexp.MustCompile(`(?s)export const SEARCH_FIELDS[^=]*=\s*\[(.*?)\]`).FindSubmatch(raw)
	require.NotNil(t, block, "SEARCH_FIELDS array not found in %s", path)

	var fields []string
	for _, m := range regexp.MustCompile(`'([a-z0-9_]+)'`).FindAllSubmatch(block[1], -1) {
		fields = append(fields, string(m[1]))
	}
	require.NotEmpty(t, fields, "parsed SEARCH_FIELDS but found no field names — the regex has "+
		"stopped matching the file's format and this test is now vacuous")
	return fields
}

// TestUISearchFields_AreAllFilterableByTheBackend is a conformance test across
// the language boundary.
//
// The search bar and the backend filter each carry their own list of field
// names, and nothing held them against each other. They disagreed on THIRTEEN
// names. Every one of them parsed cleanly in the UI, travelled to the server as
// a well-formed filter, fell through fieldMatchesValue's unknown-field default,
// and came back as "no books found":
//
//	year  series_number  isbn10  isbn13  work_id  channels  bit_depth
//	created_at  updated_at  duration  file_size  bitrate  sample_rate
//
// The last four are the sharpest: the backend implemented duration_seconds,
// file_size_bytes, bitrate_kbps and sample_rate_hz — the same columns under
// unit-suffixed names the UI never sends. Measured on production 2026-08-14,
// duration:1 answered 0 while duration_seconds:1 answered 25,090.
//
// A test of either side alone could not have caught this. Whoever wrote the UI
// list wrote it correctly for the UI; whoever wrote the Go switch wrote it
// correctly for Go. Only holding the two against each other finds the gap.
func TestUISearchFields_AreAllFilterableByTheBackend(t *testing.T) {
	uiFields := readUISearchFields(t)

	// Non-vacuity: guard the exemption list from swallowing the whole set.
	require.Greater(t, len(uiFields), len(uiSearchFieldsExemptFromBackendFilter)+10,
		"almost everything is exempt — the exemption list has become the test")

	checked := 0
	for _, field := range uiFields {
		if why, exempt := uiSearchFieldsExemptFromBackendFilter[field]; exempt {
			t.Logf("exempt: %s (%s)", field, why)
			continue
		}
		checked++
		require.Truef(t, FieldIsKnown(field),
			"the search bar offers %q but the backend filter does not implement it: a user typing "+
				"%s:something gets a well-formed request that matches no books and reports count 0, "+
				"which reads as \"none exist\". Either implement it in bookFieldValue or remove it "+
				"from SEARCH_FIELDS in %s.", field, field, searchParserPath)
	}
	require.Greater(t, checked, 30, "expected the UI to offer well over 30 filterable fields")
}

// TestFilterFieldNames_MatchTheMatcher holds the advertised name list against
// the matcher that has to honour it.
//
// KnownFilterFields is what the rejection message shows a caller who used a bad
// field name, so a name listed there but not implemented would be advice that
// produces zero results — worse than the error it replaced. Go cannot enumerate
// a switch's case labels, so allFilterFieldNames is the one unavoidable second
// copy of the field set; this test is what keeps the copy honest.
func TestFilterFieldNames_MatchTheMatcher(t *testing.T) {
	names := KnownFilterFields()
	require.NotEmpty(t, names)

	for _, name := range names {
		require.Truef(t, FieldIsKnown(name),
			"%q is advertised by KnownFilterFields but FieldIsKnown rejects it — the rejection "+
				"message would recommend a field that matches nothing", name)
	}

	// And the reverse direction, spot-checked: a name the matcher accepts but
	// the list omits would never be suggested to a caller who needs it.
	for _, implemented := range []string{"title", "year", "duration", "marked_for_deletion", "isbn13"} {
		require.Containsf(t, names, implemented,
			"%q is filterable but missing from KnownFilterFields", implemented)
	}

	require.False(t, FieldIsKnown("zzz_not_a_real_field"),
		"an unknown field must be reported unknown, or the boundary rejection never fires")
}
