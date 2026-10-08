// file: internal/itunes/service/importer_execute_test.go
// version: 1.4.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-09-24

package itunesservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RecordITLReadTime + CheckITLConflict
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// newImporter
// ---------------------------------------------------------------------------

func TestNewImporter_NilOptional(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	imp := newImporter(Deps{
		Store:  m,
		Config: Config{},
	})
	require.NotNil(t, imp)
	assert.NotNil(t, imp.store)
}

// ---------------------------------------------------------------------------
// Execute — empty library (no audiobook groups) → early return nil
// ---------------------------------------------------------------------------

func TestExecute_EmptyLibrary(t *testing.T) {
	// Write a minimal XML with only a non-audiobook track so Execute
	// parses successfully but finds zero audiobook groups.
	xmlContent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
		<key>1</key>
		<dict>
			<key>Track ID</key><integer>1</integer>
			<key>Name</key><string>Music Track</string>
			<key>Artist</key><string>Some Band</string>
			<key>Genre</key><string>Rock</string>
			<key>Kind</key><string>MPEG audio file</string>
			<key>Location</key><string>file:///Users/test/Music/track.mp3</string>
		</dict>
	</dict>
	<key>Playlists</key>
	<array/>
</dict>
</plist>`

	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "iTunes Library.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte(xmlContent), 0o644))

	m := dbmocks.NewMockStore(t)
	// Execute calls SaveParams → SaveOperationParams, then LoadCheckpoint → GetOperationState
	// With zero groups, it calls ClearState → DeleteOperationState and returns nil.
	m.EXPECT().SaveOperationParams("test-op", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("test-op").Return(nil, nil).Once()
	m.EXPECT().DeleteOperationState("test-op").Return(nil).Once()

	imp := newImporter(Deps{
		Store:  m,
		Config: Config{},
	})

	log := logger.New("test")
	err := imp.Execute(context.Background(), "test-op", ImportRequest{
		LibraryPath: xmlPath,
		ImportMode:  "import",
	}, log)

	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Execute — library parse failure → error returned
// ---------------------------------------------------------------------------

func TestExecute_ParseFailure(t *testing.T) {
	dir := t.TempDir()
	badXMLPath := filepath.Join(dir, "bad.xml")
	require.NoError(t, os.WriteFile(badXMLPath, []byte("not xml at all"), 0o644))

	m := dbmocks.NewMockStore(t)
	m.EXPECT().SaveOperationParams("op-fail", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("op-fail").Return(nil, nil).Once()
	m.EXPECT().DeleteOperationState("op-fail").Return(nil).Once()

	imp := newImporter(Deps{Store: m, Config: Config{}})
	log := logger.New("test")
	err := imp.Execute(context.Background(), "op-fail", ImportRequest{
		LibraryPath: badXMLPath,
		ImportMode:  "import",
	}, log)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse library")
}

// ---------------------------------------------------------------------------
// Sync — empty library (no audiobook groups) → early return nil, no store calls
// ---------------------------------------------------------------------------

func TestSync_EmptyLibrary(t *testing.T) {
	enableSyncForTest(t)
	xmlContent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
		<key>1</key>
		<dict>
			<key>Track ID</key><integer>1</integer>
			<key>Name</key><string>Pop Song</string>
			<key>Genre</key><string>Pop</string>
			<key>Kind</key><string>MPEG audio file</string>
			<key>Location</key><string>file:///Users/test/Music/song.mp3</string>
		</dict>
	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>`

	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "iTunes Library.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte(xmlContent), 0o644))

	// No store is needed — Sync returns before any store access when totalGroups == 0
	imp := &Importer{cfg: Config{}}
	log := logger.New("test")
	err := imp.Sync(context.Background(), xmlPath, nil, nil, log)
	assert.NoError(t, err)
}

func TestSync_ParseFailure(t *testing.T) {
	enableSyncForTest(t)
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.xml")
	require.NoError(t, os.WriteFile(badPath, []byte("not xml"), 0o644))

	imp := &Importer{cfg: Config{}}
	log := logger.New("test")
	err := imp.Sync(context.Background(), badPath, nil, nil, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse library")
}
