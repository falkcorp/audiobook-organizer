// file: internal/itunes/source_fields_test.go
// version: 1.0.0
// guid: 6962328d-38b0-4622-ad29-190879f0ac5e
// last-edited: 2026-09-11

package itunes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseLibrary_ITLDeclaresNoBookmark pins the parser side of the
// SourceFields contract. The ITL path decodes play count and last-played but
// no bookmark, and it must say so, otherwise the importer writes its 0 over a
// stored bookmark.
func TestParseLibrary_ITLDeclaresNoBookmark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iTunes Library.itl")
	require.NoError(t, os.WriteFile(path, buildConvertTestITL(t), 0o644))

	lib, err := ParseLibrary(path) // auto-detects the hdfm magic
	require.NoError(t, err)
	require.NotEmpty(t, lib.Tracks)

	assert.Equal(t, ITLSourceFields(), lib.Carries)
	assert.False(t, lib.Carries.Bookmark, "ITL decodes no bookmark")
	assert.True(t, lib.Carries.PlayCount, "ITL decodes play count at mhit offset 76")
	assert.True(t, lib.Carries.PlayDate, "ITL decodes last-played at mhit offset 100")
	for _, tr := range lib.Tracks {
		assert.Zero(t, tr.Bookmark, "an ITL track's Bookmark is always 0 (unknown)")
	}
}

// TestParseLibrary_XMLDeclaresAllPlaybackFields covers the XML side, using a
// track with no Bookmark key: that is how iTunes writes a track whose bookmark
// was cleared, and it decodes to 0, which the importer must treat as a reset.
func TestParseLibrary_XMLDeclaresAllPlaybackFields(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
		<key>7</key>
		<dict>
			<key>Track ID</key><integer>7</integer>
			<key>Persistent ID</key><string>0011223344556677</string>
			<key>Name</key><string>Some Book</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Play Count</key><integer>2</integer>
			<key>Location</key><string>file://localhost/books/Audiobooks/some-book.m4b</string>
		</dict>
	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>`
	path := filepath.Join(t.TempDir(), "iTunes Library.xml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))

	lib, err := ParseLibrary(path)
	require.NoError(t, err)
	require.Len(t, lib.Tracks, 1)

	assert.Equal(t, XMLSourceFields(), lib.Carries)
	for _, tr := range lib.Tracks {
		assert.Equal(t, 2, tr.PlayCount)
		assert.Zero(t, tr.Bookmark, "an absent Bookmark key decodes to 0")
	}
}

// TestLibrary_ZeroValueCarriesNothing documents the fail-closed default: a
// Library literal that never declares its capabilities writes no playback
// field.
func TestLibrary_ZeroValueCarriesNothing(t *testing.T) {
	assert.Equal(t, SourceFields{}, (&Library{}).Carries)
}
