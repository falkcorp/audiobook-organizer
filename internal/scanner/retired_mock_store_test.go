// file: internal/scanner/retired_mock_store_test.go
// version: 1.0.0
// guid: 7b3e1f84-52a9-4d06-8c71-e94a0d2b6f35
// last-edited: 2026-10-26

package scanner

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A scanner test on the hand-written MockStore reaches the retired-content
// check as soon as it stubs the complete per-hash read: the mock then reports
// that a retired row can exist (HasRetiredVersions), and a retired row in the
// stub is a retired hit rather than a brand-new book. Before 2026-10-26 the mock
// answered "no retired version" unconditionally, so the check was unreachable
// from any MockStore test.
func TestLookupScanExisting_MockStoreStubbedHashReadReachesTheRetiredCheck(t *testing.T) {
	mock := &database.MockStore{
		BookFilesWithHashFunc: func(hash string) ([]database.BookFile, error) {
			return []database.BookFile{{ID: "file-1", BookID: "holder-1", VersionID: "version-1", Retired: true, FileHash: hash}}, nil
		},
	}
	useScannerStore(t, mock)

	hash := "retired-content-hash"
	e, err := lookupScanExisting(&Book{FilePath: "/lib/Gotham/Hush/01.mp3"}, fileOwnershipVerdict{}, &hash)
	require.NoError(t, err)
	require.NotNil(t, e.retired, "the stubbed retired row must be judged")
	require.Equal(t, "holder-1", e.retired.holder)
	require.Equal(t, "version-1", e.retired.versionID)
}

// And a MockStore with no stubs still imports a hashed file as new: it holds no
// row, so there is nothing retired for the check to find.
func TestLookupScanExisting_MockStoreWithoutStubsIsNotARetiredHit(t *testing.T) {
	useScannerStore(t, &database.MockStore{})

	hash := "fresh-content-hash"
	e, err := lookupScanExisting(&Book{FilePath: "/lib/Gotham/Hush/01.mp3"}, fileOwnershipVerdict{}, &hash)
	require.NoError(t, err)
	require.Nil(t, e.retired)
}
