// file: internal/itunes/plist_streaming_parse_test.go
// version: 1.0.0
// guid: 2e8b0c47-9a13-4d65-8f72-6c41b9e35a08
// last-edited: 2026-08-13

package itunes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// StreamingParseLibrary had no test coverage of any kind, which is how it came
// to return (0, nil) — success, zero tracks, no warning — against a normally
// formatted iTunes Library.xml. Production logs
// "BackfillITunesTrackPIDs completed stream parsing tracks_processed=0
// registered=0", the shape of a whole backfill silently doing nothing.
//
// testdata/test_library.xml is in Apple's real layout, where the Tracks key
// and its dict are on separate lines:
//
//	<key>Tracks</key>
//	<dict>
//
// which means the token immediately after CharData("Tracks") is
// EndElement(key), not the StartElement the scanner required.
func TestStreamingParseLibrary_YieldsTracks(t *testing.T) {
	var seen []*Track
	n, err := StreamingParseLibrary(context.Background(), "testdata/test_library.xml", func(tr *Track) error {
		seen = append(seen, tr)
		return nil
	})
	require.NoError(t, err)

	// The fixture holds real track entries; the parser must surface them.
	// Asserting >0 rather than an exact count keeps this from breaking when
	// the fixture grows, while still failing hard on the silent no-op.
	require.NotZero(t, n, "StreamingParseLibrary reported success but parsed zero tracks")
	require.NotEmpty(t, seen, "onTrack callback was never invoked")
	require.Equal(t, n, len(seen), "returned count must match callback invocations")

	// Pin one known row so a parser that yields empty Track structs cannot
	// pass on count alone.
	var hobbit *Track
	for _, tr := range seen {
		if tr.PersistentID == "ABCD1234EFGH5678" {
			hobbit = tr
			break
		}
	}
	require.NotNil(t, hobbit, "expected the fixture's Persistent ID ABCD1234EFGH5678")
	require.Equal(t, "The Hobbit", hobbit.Name)
	require.Equal(t, "Middle-earth, Book 1", hobbit.Album)
}

// TestStreamingParseLibrary_MissingFileErrors is the control: a genuine
// failure must still be reported as one. If this and the test above were both
// green before the fix, the parser would be indistinguishable from a function
// that never reads anything.
func TestStreamingParseLibrary_MissingFileErrors(t *testing.T) {
	n, err := StreamingParseLibrary(context.Background(), "testdata/does_not_exist.xml", func(*Track) error {
		return nil
	})
	require.Error(t, err, "a missing library file must be an error, not a silent zero")
	require.Zero(t, n)
}
