// file: internal/itunes/service/importer_primary_version_test.go
// version: 1.0.0
// guid: bdb97ea5-c7e2-4797-aec0-a58b225d5fdb
// last-edited: 2026-08-13

// Regression test for the version-group primary defect fixed 2026-08-13.
//
// The importer's create path minted a fresh, unique VersionGroupID for each
// newly-imported book and then marked that book IsPrimaryVersion=false. Since
// the group was brand new the book was its only member, so the group elected no
// primary at all. Every client applying the default is_primary_version=true
// filter — which the web Library page does — could then never see the book,
// while API clients applying no filter saw it fine. That split is what made it
// present as "search is broken in the browser but works in the app".
//
// 479 groups (724 books) reached production in this state before the fix.

package itunesservice

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestExecute_NewBookIsPrimaryOfItsOwnVersionGroup asserts the actual payload
// handed to CreateBook rather than merely that CreateBook was called.
// mock.Anything would have accepted the buggy book just as happily as the
// fixed one — the whole defect lives in a field value, so the field value is
// what the test has to read.
//
// CreateBook is stubbed to fail on purpose: the flags under test are set
// immediately before the call, so capturing the argument and then erroring out
// exercises exactly the code path in question without needing to mock the
// entire post-create enrichment chain.
func TestExecute_NewBookIsPrimaryOfItsOwnVersionGroup(t *testing.T) {
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "primary-chapter.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("p"), 512), 0o644))

	pid := "PRIMARY_VERSION_PID"
	xmlPath := writeXMLWithAudiobook(t, dir, "Primary Book", "Primary Author", pid, trackPath)

	authorRecord := &database.Author{ID: 7, Name: "Primary Author"}

	var captured *database.Book
	m := dbmocks.NewMockStore(t)
	m.EXPECT().SaveOperationParams("op-primary", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("op-primary").Return(nil, nil).Once()
	m.EXPECT().GetAuthorByName(mock.Anything).Return(authorRecord, nil).Maybe()
	m.EXPECT().GetSeriesByName(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().CreateSeries(mock.Anything, mock.Anything).Return(&database.Series{ID: 7, Name: "Primary Book"}, nil).Maybe()
	m.EXPECT().IsExternalIDTombstoned("itunes", pid).Return(false, nil).Once()
	m.EXPECT().GetBookByExternalID("itunes", pid).Return("", fmt.Errorf("not found")).Once()
	m.EXPECT().CreateBook(mock.Anything).
		Run(func(book *database.Book) { captured = book }).
		Return(nil, fmt.Errorf("stop after capture")).Once()
	m.EXPECT().DeleteOperationState("op-primary").Return(nil).Once()
	m.EXPECT().SaveLibraryFingerprint(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	imp := newImporter(Deps{Store: m, Config: Config{}})
	log := logger.New("test")
	err := imp.Execute(context.Background(), "op-primary", ImportRequest{
		LibraryPath: xmlPath,
		ImportMode:  "import",
	}, log)
	assert.NoError(t, err, "a single failed save must not fail the whole import")

	require.NotNil(t, captured, "CreateBook was never called — the import never reached the create path")

	require.NotNil(t, captured.VersionGroupID, "VersionGroupID must be set on a newly imported book")
	assert.True(t, strings.HasPrefix(*captured.VersionGroupID, "vg-"),
		"VersionGroupID = %q, want a vg- prefixed id", *captured.VersionGroupID)

	require.NotNil(t, captured.IsPrimaryVersion, "IsPrimaryVersion must be set explicitly, not left nil")
	assert.True(t, *captured.IsPrimaryVersion,
		"a book that is the sole member of a freshly-minted version group must be that group's primary; "+
			"marking it non-primary leaves the group with no primary and hides the book from the web UI")
}
