// file: internal/books/store/modify_book_respecting_locks_test.go
// version: 1.0.0
// guid: dac61686-50f4-43f7-a44a-0dd3cdc8238f
// last-edited: 2026-11-18

package store

import (
	"errors"
	"testing"
)

// lockFixture is one stored book with a title, a narrator and a series, whose
// user-locked fields are the given keys. Every committed write is captured.
func lockFixture(t *testing.T, locked []string, lockErr error) (*MockStore, *[]Book) {
	t.Helper()
	seq := 3
	sid := 7
	narr := "Alfred Pennyworth"
	stored := Book{ID: "bk", Title: "Year One", Narrator: &narr, SeriesID: &sid, SeriesSequence: &seq}
	var writes []Book
	return &MockStore{
		GetBookByIDFunc: func(id string) (*Book, error) {
			cp := stored
			return &cp, nil
		},
		UpdateBookFunc: func(id string, b *Book) (*Book, error) {
			writes = append(writes, *b)
			return b, nil
		},
		GetMetadataFieldStatesFunc: func(id string) ([]MetadataFieldState, error) {
			if lockErr != nil {
				return nil, lockErr
			}
			rows := make([]MetadataFieldState, 0, len(locked))
			for _, k := range locked {
				rows = append(rows, MetadataFieldState{BookID: id, Field: k, OverrideLocked: true})
			}
			return rows, nil
		},
	}, &writes
}

func retitleAndRenarrate(b *Book) error {
	n := "Harvey Dent"
	b.Title = "Year Two"
	b.Narrator = &n
	return nil
}

// A locked column in a multi-column write holds the WHOLE write: the unlocked
// column is not written either, and the held key is reported.
func TestModifyBookRespectingLocks_LockedColumnHoldsTheWholeWrite(t *testing.T) {
	st, writes := lockFixture(t, []string{FieldKeyTitle}, nil)
	written, held, err := ModifyBookRespectingLocks(st, st, "bk", retitleAndRenarrate)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(*writes) != 0 {
		t.Fatalf("%d writes, want 0: a locked title must hold the narrator write too", len(*writes))
	}
	if len(held) != 1 || held[0] != FieldKeyTitle {
		t.Fatalf("held = %v, want [title]", held)
	}
	if written == nil || written.Title != "Year One" || *written.Narrator != "Alfred Pennyworth" {
		t.Fatalf("returned row = %+v, want the row as read", written)
	}
}

// The control: the same write on an unlocked book lands in full, so the test
// above cannot be satisfied by a helper that writes nothing.
func TestModifyBookRespectingLocks_UnlockedBookIsWritten(t *testing.T) {
	st, writes := lockFixture(t, nil, nil)
	_, held, err := ModifyBookRespectingLocks(st, st, "bk", retitleAndRenarrate)
	if err != nil || len(held) != 0 {
		t.Fatalf("err = %v held = %v", err, held)
	}
	if len(*writes) != 1 || (*writes)[0].Title != "Year Two" || *(*writes)[0].Narrator != "Harvey Dent" {
		t.Fatalf("writes = %+v, want one full write", *writes)
	}
}

// A lock on a column the write does not touch does not hold it.
func TestModifyBookRespectingLocks_LockOnUntouchedColumnDoesNotHold(t *testing.T) {
	st, writes := lockFixture(t, []string{FieldKeyPublisher}, nil)
	_, held, err := ModifyBookRespectingLocks(st, st, "bk", retitleAndRenarrate)
	if err != nil || len(held) != 0 {
		t.Fatalf("err = %v held = %v", err, held)
	}
	if len(*writes) != 1 {
		t.Fatalf("%d writes, want 1", len(*writes))
	}
}

// A write that clears the series link and position is held by the series lock.
func TestModifyBookRespectingLocks_SeriesLockHoldsAnUnlink(t *testing.T) {
	st, writes := lockFixture(t, []string{FieldKeySeriesName}, nil)
	_, held, err := ModifyBookRespectingLocks(st, st, "bk", func(b *Book) error {
		b.SeriesID, b.SeriesSequence = nil, nil
		return nil
	})
	if err != nil || len(*writes) != 0 {
		t.Fatalf("err = %v writes = %d", err, len(*writes))
	}
	if len(held) != 1 || held[0] != FieldKeySeriesName {
		t.Fatalf("held = %v, want [series_name]", held)
	}
}

// Locks unreadable: fail closed, mutate never runs, nothing is written.
func TestModifyBookRespectingLocks_LockReadErrorWritesNothing(t *testing.T) {
	st, writes := lockFixture(t, nil, errors.New("pebble: closed"))
	ran := false
	_, _, err := ModifyBookRespectingLocks(st, st, "bk", func(b *Book) error { ran = true; return nil })
	if !errors.Is(err, ErrFieldLocksUnavailable) {
		t.Fatalf("err = %v, want ErrFieldLocksUnavailable", err)
	}
	if ran || len(*writes) != 0 {
		t.Fatalf("mutate ran = %v, writes = %d: a book whose locks cannot be read must not be touched", ran, len(*writes))
	}
}

// mutate's own errors pass through untouched; ErrSkipBookWrite is a clean no-op.
func TestModifyBookRespectingLocks_MutateErrorsPassThrough(t *testing.T) {
	st, writes := lockFixture(t, nil, nil)
	boom := errors.New("boom")
	if _, _, err := ModifyBookRespectingLocks(st, st, "bk", func(*Book) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if _, held, err := ModifyBookRespectingLocks(st, st, "bk", func(*Book) error { return ErrSkipBookWrite }); err != nil || len(held) != 0 {
		t.Fatalf("skip: err = %v held = %v", err, held)
	}
	if len(*writes) != 0 {
		t.Fatalf("%d writes, want 0", len(*writes))
	}
}

// A missing book is (nil, nil, nil), as ModifyBook says; the lock read is not reached.
func TestModifyBookRespectingLocks_MissingBook(t *testing.T) {
	st, _ := lockFixture(t, nil, nil)
	st.GetBookByIDFunc = func(string) (*Book, error) { return nil, nil }
	written, held, err := ModifyBookRespectingLocks(st, st, "gone", retitleAndRenarrate)
	if written != nil || held != nil || err != nil {
		t.Fatalf("got (%v, %v, %v), want all nil", written, held, err)
	}
}
