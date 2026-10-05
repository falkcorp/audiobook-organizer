// file: internal/authority/authoritybuild/seed_test.go
// version: 1.1.0
// guid: 8ac98f24-09ff-4862-bdb2-36d9269ae71a
// last-edited: 2026-10-05

package authoritybuild

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
)

func TestSeed_EmbeddedDecodesAndValidates(t *testing.T) {
	s, err := LoadSeed()
	require.NoError(t, err)
	require.Equal(t, "internal/authority/authoritybuild/seed/authority_seed.json", s.File)
	counts := map[string]int{}
	for _, e := range s.Entries {
		counts[e.Kind]++
	}
	require.Positive(t, counts[SeedKindAuthor])
	require.Positive(t, counts[SeedKindNarrator])
	require.Positive(t, counts[SeedKindPublisher])
	// Sorted by (kind, fold, name): the generator's order, so a regeneration
	// diffs line by line and a hand edit out of order is caught.
	require.True(t, sort.SliceIsSorted(s.Entries, func(i, j int) bool {
		a, b := s.Entries[i], s.Entries[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return authority.Fold(a.Name) < authority.Fold(b.Name)
	}))
}

// TestSeed_HasOnlyAllowedFields is the shape half of the no-title guard: the
// seed may carry names, contributor ASINs and two role flags, nothing else.
// A "title", "asin" (product), "series", "sku" or count field fails here even
// if DecodeSeed were loosened.
func TestSeed_HasOnlyAllowedFields(t *testing.T) {
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(seedJSON, &top))
	require.ElementsMatch(t, []string{"file", "version", "guid", "last_edited", "source", "tier", "entries"}, keysOf(top))

	var entries []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["entries"], &entries))
	allowed := []string{"kind", "name", "asins", "also_narrator"}
	for i, e := range entries {
		require.ElementsMatch(t, allowed, keysOf(e), "entry %d", i)
	}
}

// titleShapeRe matches the shapes a book title or series position takes and
// a contributor name does not.
var titleShapeRe = regexp.MustCompile(`(?i)[:#]|\b(book|volume|vol|part|episode|chapter)\s*\d|\(\s*(unabridged|abridged|dramati[sz]ed)\s*\)`)

// TestSeed_NoTitleShapedNames is the content half of the no-title guard. The
// generator already drops any name that fold-equals a title in the export it
// read; this catches a title that reached the file another way.
func TestSeed_NoTitleShapedNames(t *testing.T) {
	s, err := LoadSeed()
	require.NoError(t, err)
	for _, e := range s.Entries {
		require.False(t, titleShapeRe.MatchString(e.Name), "seed name %q has a title shape", e.Name)
		require.LessOrEqual(t, len(strings.Fields(e.Name)), 8, "seed name %q is too long for a name", e.Name)
	}
}

func TestDecodeSeed_RejectsBadShapes(t *testing.T) {
	head := `{"file":"f","version":"1.0.0","guid":"g","last_edited":"d","source":"owner_library_seed","tier":"O","entries":[`
	ok := `{"kind":"author","name":"Ann Leckie","asins":["B001JP7W9E"],"also_narrator":false}`
	_, err := DecodeSeed([]byte(head + ok + `]}`))
	require.NoError(t, err)

	for name, body := range map[string]string{
		"title field":    `{"kind":"author","name":"Ann Leckie","asins":[],"also_narrator":false,"title":"Ancillary Justice"}`,
		"unknown kind":   `{"kind":"series","name":"Imperial Radch","asins":[],"also_narrator":false}`,
		"collective":     `{"kind":"narrator","name":"Full Cast","asins":[],"also_narrator":false}`,
		"bad asin":       `{"kind":"author","name":"Ann Leckie","asins":["not-an-asin"],"also_narrator":false}`,
		"publisher asin": `{"kind":"publisher","name":"Orbit","asins":["B001JP7W9E"],"also_narrator":false}`,
		"duplicate":      ok + `,{"kind":"author","name":"ANN LECKIE","asins":[],"also_narrator":false}`,
		"empty fold":     `{"kind":"author","name":"...","asins":[],"also_narrator":false}`,
	} {
		_, err := DecodeSeed([]byte(head + body + `]}`))
		require.Error(t, err, name)
	}
	_, err = DecodeSeed([]byte(strings.Replace(head, `"tier":"O"`, `"tier":"A"`, 1) + ok + `]}`))
	require.Error(t, err, "seed tier must be O")
	_, err = DecodeSeed([]byte(strings.Replace(head, `"file":"f"`, `"file":"f","count":3`, 1) + ok + `]}`))
	require.Error(t, err, "unknown top-level field")
}

// TestSeed_AuthorsAreNotCastOnlyUnderGoRules checks the Python generator's
// cast rule against this package's (IsCastContext) on a real export. The
// export holds titles, so it is never committed: the test runs only when
// AUTHORITY_EXPORT_PATH points at one.
func TestSeed_AuthorsAreNotCastOnlyUnderGoRules(t *testing.T) {
	path := os.Getenv("AUTHORITY_EXPORT_PATH")
	if path == "" {
		t.Skip("AUTHORITY_EXPORT_PATH not set")
	}
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	items, err := ReadLibraryExport(f, MaxLibraryExportBytes)
	require.NoError(t, err)
	b := NewBuilder()
	for i, it := range items {
		require.NoError(t, b.AddRawProduct(authority.SourceLibraryExport, ExportTiebreak(i), it))
	}
	res := b.Finish()
	require.Zero(t, res.Report.Sources[authority.SourceLibraryExport].Undecodable)

	s, err := LoadSeed()
	require.NoError(t, err)
	for _, e := range s.Entries {
		p := res.Persons[authority.Fold(e.Name)]
		switch {
		case e.Kind == SeedKindAuthor:
			require.NotNil(t, p, e.Name)
			_, author := p.Roles[authority.RoleAuthor]
			require.True(t, author, "seed lists %q as an author but Go's rules see only cast credits", e.Name)
		case e.Kind == SeedKindNarrator:
			require.NotNil(t, p, e.Name)
			_, author := p.Roles[authority.RoleAuthor]
			require.False(t, author, "seed lists %q as narrator-only but Go's rules see a plain author credit", e.Name)
		}
	}
}

// TestFold_MatchesAuthorcredit: the leaf folds exactly as authorcredit does
// (it lives here because a leaf test importing authorcredit would cycle once
// authorcredit consumes the leaf).
func TestFold_MatchesAuthorcredit(t *testing.T) {
	for _, s := range []string{"J.N. Chaney", "Zoë  Ståhl", "Ray Porter", "李 小龙", "..."} {
		require.Equal(t, authorcredit.LettersKey(s), authority.Fold(s), s)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
