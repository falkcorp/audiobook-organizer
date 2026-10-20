// file: internal/repairs/writer_supersede_test.go
// version: 1.0.1
// guid: bef3b82c-20e5-47e0-842a-047e9b961426
// last-edited: 2026-10-20

package repairs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// reverterJournal is the credit fake's op journal plus the ChangeReverter
// capability, recording what was marked.
type reverterJournal struct {
	creditFake
	marked map[string][]string
	err    error
}

func (j *reverterJournal) MarkOperationChangesReverted(op string, ids []string) error {
	if j.err != nil {
		return j.err
	}
	j.marked[op] = append(j.marked[op], ids...)
	return nil
}

// TestWriterSupersedeChanges: another op's rows are marked through the
// journal's ChangeReverter; the writer's own op, an empty op id, and a
// journal without the capability are refused with nothing marked.
func TestWriterSupersedeChanges(t *testing.T) {
	t.Parallel()
	j := &reverterJournal{credits: map[string][]database.BookAuthor{}, marked: map[string][]string{}}
	w := NewWriter(newMemStore(), newMemStore(), "fx", "bulk_update", "rp-").WithJournal(nil, j, "op-2")

	require.NoError(t, w.SupersedeChanges("op-1", []string{"a", "b"}))
	assert.Equal(t, map[string][]string{"op-1": {"a", "b"}}, j.marked)
	require.NoError(t, w.SupersedeChanges("op-1", nil), "nothing to mark")
	assert.Error(t, w.SupersedeChanges("op-2", []string{"c"}), "its own op")
	assert.Error(t, w.SupersedeChanges("", []string{"c"}), "no op")
	assert.Equal(t, map[string][]string{"op-1": {"a", "b"}}, j.marked)

	j.err = errors.New("disk full")
	assert.ErrorContains(t, w.SupersedeChanges("op-1", []string{"d"}), "disk full")

	plain := &creditFake{credits: map[string][]database.BookAuthor{}}
	w2 := NewWriter(newMemStore(), newMemStore(), "fx", "bulk_update", "rp-").WithJournal(nil, plain, "op-2")
	assert.ErrorIs(t, w2.SupersedeChanges("op-1", []string{"a"}), ErrSupersedeUnsupported)
}
