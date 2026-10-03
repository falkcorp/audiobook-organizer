// file: internal/repairs/writer_credits_test.go
// version: 1.2.0
// guid: 1b72196d-b654-4270-bd15-c54923102210
// last-edited: 2026-10-03

package repairs

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// creditFake is a credit junction plus an op journal. journalLog records the
// order of journal rows and junction writes, so a test can prove the row
// comes first.
type creditFake struct {
	mu         sync.Mutex
	credits    map[string][]database.BookAuthor
	journal    []database.OperationChange
	order      []string
	failJournl bool
}

func (f *creditFake) ModifyBookAuthors(id string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
	f.mu.Lock()
	cur := append([]database.BookAuthor(nil), f.credits[id]...)
	f.mu.Unlock()
	next, err := fn(cur)
	if err != nil {
		if errors.Is(err, database.ErrSkipBookAuthorsWrite) {
			return cur, nil
		}
		return nil, err
	}
	f.mu.Lock()
	f.credits[id] = next
	f.order = append(f.order, "write:"+id)
	f.mu.Unlock()
	return next, nil
}

func (f *creditFake) CreateOperationChange(c *database.OperationChange) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failJournl {
		return errors.New("journal down")
	}
	f.journal = append(f.journal, *c)
	f.order = append(f.order, "journal:"+c.BookID)
	return nil
}

func (f *creditFake) GetOperationChanges(opID string) ([]*database.OperationChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*database.OperationChange
	for i := range f.journal {
		if f.journal[i].OperationID == opID {
			c := f.journal[i]
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *creditFake) MarkOperationChangesReverted(opID string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	now := time.Now()
	for i := range f.journal {
		if f.journal[i].OperationID == opID && want[f.journal[i].ID] {
			f.journal[i].RevertedAt = &now
		}
	}
	return nil
}

// credWriter wires a Writer with the op journal and the credit store.
func credWriter(s *memStore, f *creditFake) *Writer {
	return NewWriter(s, s, "junk", "bulk_update", "rp-").WithJournal(nil, f, "op-1").WithCredits(f)
}

func TestWriter_ModifyCredits_JournalsFirstUnderLock(t *testing.T) {
	s := newMemStore()
	f := &creditFake{credits: map[string][]database.BookAuthor{"b1": {{BookID: "b1", AuthorID: 7, Role: "author"}}}}
	w := credWriter(s, f)
	next, err := w.ModifyCredits("b1", func(cur []database.BookAuthor) ([]database.BookAuthor, UndoEntry, error) {
		require.Len(t, cur, 1, "fn sees the junction the write replaces")
		return []database.BookAuthor{{BookID: "b1", AuthorID: 9, Role: "author"}},
			UndoEntry{ChangeType: "junk_author_credits", Field: "book_authors", Old: "old", New: "new"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 9, next[0].AuthorID)
	require.Equal(t, []string{"journal:b1", "write:b1"}, f.order)
	require.Len(t, f.journal, 1)
	require.Equal(t, "op-1", f.journal[0].OperationID)
	require.Equal(t, 1, w.Journaled())
	require.Equal(t, 1, w.Writes())
}

func TestWriter_ModifyCredits_JournalFailureWritesNothing(t *testing.T) {
	s := newMemStore()
	orig := []database.BookAuthor{{BookID: "b1", AuthorID: 7, Role: "author"}}
	f := &creditFake{credits: map[string][]database.BookAuthor{"b1": orig}, failJournl: true}
	w := credWriter(s, f)
	_, err := w.ModifyCredits("b1", func(cur []database.BookAuthor) ([]database.BookAuthor, UndoEntry, error) {
		return nil, UndoEntry{ChangeType: "x"}, nil
	})
	require.ErrorIs(t, err, ErrNotJournaled)
	require.Equal(t, orig, f.credits["b1"], "no journal row, no write")
	require.Empty(t, f.order)
}

func TestWriter_ModifyCredits_SkipAndRefusal(t *testing.T) {
	s := newMemStore()
	f := &creditFake{credits: map[string][]database.BookAuthor{"b1": nil}}
	w := credWriter(s, f)
	_, err := w.ModifyCredits("b1", func([]database.BookAuthor) ([]database.BookAuthor, UndoEntry, error) {
		return nil, UndoEntry{}, database.ErrSkipBookAuthorsWrite
	})
	require.NoError(t, err)
	_, err = w.ModifyCredits("b1", func([]database.BookAuthor) ([]database.BookAuthor, UndoEntry, error) {
		return nil, UndoEntry{}, ErrChangedSincePlan
	})
	require.ErrorIs(t, err, ErrChangedSincePlan)
	require.Empty(t, f.journal)
	require.Equal(t, 0, w.Writes())

	// Unwired: refused, never a silent success.
	bare := NewWriter(s, s, "junk", "bulk_update", "rp-")
	_, err = bare.ModifyCredits("b1", func([]database.BookAuthor) ([]database.BookAuthor, UndoEntry, error) {
		return nil, UndoEntry{}, nil
	})
	require.Error(t, err)
	require.ErrorIs(t, bare.RecordChange("b1", UndoEntry{ChangeType: "x"}), ErrNotJournaled)
}

// SetPrimaryAuthor's history batch is marked apply_op_journaled when the
// writer journaled the book's credit move (undo-last-apply refuses it: the
// junction lives in the op journal), and apply_incomplete when it journaled
// nothing for the book (refused too).
func TestWriter_SetPrimaryAuthor(t *testing.T) {
	marker := func(s *memStore) string {
		m := ""
		for _, h := range s.history {
			if h.Field == "apply" {
				m = h.ChangeType
			}
		}
		return m
	}
	s := newMemStore()
	s.add("b1", "T", "/lib/t.m4b", nil)
	junk := 7
	s.books["b1"].AuthorID = &junk
	f := &creditFake{credits: map[string][]database.BookAuthor{"b1": {{BookID: "b1", AuthorID: 7, Role: "author"}}}}
	w := credWriter(s, f)

	// Compare-and-set: a primary that is not `from` is refused.
	require.ErrorIs(t, w.SetPrimaryAuthor("b1", 8, &database.Author{ID: 9, Name: "Real"}), ErrChangedSincePlan)

	_, err := w.ModifyCredits("b1", func([]database.BookAuthor) ([]database.BookAuthor, UndoEntry, error) {
		return []database.BookAuthor{{BookID: "b1", AuthorID: 9, Role: "author"}},
			UndoEntry{ChangeType: "junk_author_credits", Field: "book_authors", Old: "o", New: "n"}, nil
	})
	require.NoError(t, err)
	require.NoError(t, w.SetPrimaryAuthor("b1", 7, &database.Author{ID: 9, Name: "Real"}))
	b, _ := s.GetBookByID("b1")
	require.Equal(t, 9, *b.AuthorID)
	var field bool
	for _, h := range s.history {
		field = field || (h.Field == "author_id" && *h.PreviousValue == `"7"` && *h.NewValue == `"9"`)
	}
	require.True(t, field, "author_id history recorded")
	require.Equal(t, ChangeTypeApplyOpJournaled, marker(s), "undo-last-apply must refuse a credit batch")

	// Nothing journaled for the book: still refused, as incomplete.
	s2 := newMemStore()
	s2.add("b2", "T", "/lib/t2.m4b", nil)
	s2.books["b2"].AuthorID = &junk
	require.NoError(t, NewWriter(s2, s2, "junk", "bulk_update", "rp-").SetPrimaryAuthor("b2", 7, nil))
	b, _ = s2.GetBookByID("b2")
	require.Nil(t, b.AuthorID, "clearing (nil) is allowed")
	require.Equal(t, ChangeTypeApplyIncomplete, marker(s2))
}
