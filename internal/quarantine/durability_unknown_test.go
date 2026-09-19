// file: internal/quarantine/durability_unknown_test.go
// version: 1.0.0
// guid: 7712d917-9c89-4f3d-bcd6-1cdf4b04ca33
// last-edited: 2026-09-19

package quarantine

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// durUnknownUpdateStore applies UpdateBookFile for real, then reports that
// its fsync failed: the row IS repointed, only its durability is unknown.
type durUnknownUpdateStore struct{ *database.PebbleStore }

func (s durUnknownUpdateStore) UpdateBookFile(id string, f *database.BookFile) error {
	if err := s.PebbleStore.UpdateBookFile(id, f); err != nil {
		return err
	}
	return fmt.Errorf("%w: injected fsync failure", database.ErrBookFileDurabilityUnknown)
}

// A repoint whose write was applied but not known durable must be treated as
// done. Treated as a failure, the pass moved the file BACK while the row
// already named the new path — a row pointing at a file that is not there.
func TestQuarantineBook_RepointDurabilityUnknownKeepsRowAndFileTogether(t *testing.T) {
	_, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)

	qs := svcWith(durUnknownUpdateStore{store}, root)
	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))

	requireRowsAt(t, store, book.ID, dst...)
	requireOnDisk(t, dst...)
	requireGone(t, src...)
}
