// file: core/backup/journal/move_test.go
// version: 1.0.0
// guid: 2e6b8d41-9a07-4c3f-b5e2-0c1d7f4a9e83
// last-edited: 2026-11-11

package journal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMoveFile: a changed backup directory moves the journal after the whole
// chain verifies; the copy is 0600 and byte-identical, the old file stays for
// the caller to remove once the record points at the new one, and the moved
// file opens and extends as the same chain.
func TestMoveFile(t *testing.T) {
	fx := newFixture(t)
	lines, _, _ := fx.built(t)
	old := fx.path
	dest := filepath.Join(t.TempDir(), "offsite", "gotham_revocations.journal")

	must(t, MoveFile(old, dest, fx.keys))
	got, err := os.ReadFile(dest)
	must(t, err)
	src, err := os.ReadFile(old)
	must(t, err)
	if !bytes.Equal(got, src) || len(strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")) != len(lines) {
		t.Fatalf("the moved journal differs from the source:\n%q\n%q", got, src)
	}
	if st, err := os.Stat(dest); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("moved journal mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
	if _, err := os.Stat(dest + ".moving"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temp copy survived: %v", err)
	}

	// The moved file is the same chain: the store's head matches it, and it
	// takes the next line.
	fx.path = dest
	j := fx.open(t)
	fx.commitRevoke(t, j, "scarecrow")
	if c := fx.chain(t); c.Break != nil || c.IntactEnd() != 4 {
		t.Errorf("moved chain: break %v, end %d; want intact at 4", c.Break, c.IntactEnd())
	}
	must(t, j.Close())

	// A crash between the rename and the record update: the destination holds
	// the verified copy (here already extended) and the old file is still the
	// recorded one; the next boot's move accepts it.
	if err := MoveFile(old, dest, fx.keys); err != nil {
		t.Errorf("a repeated move onto the verified copy: %v", err)
	}
}

// TestMoveFile_Refusals: a source that does not verify, or a destination that
// holds another journal, is refused with the old file untouched and the
// destination never overwritten; a missing source is nothing to move and is not
// created.
func TestMoveFile_Refusals(t *testing.T) {
	fx := newFixture(t)
	lines, _, _ := fx.built(t)
	dir := t.TempDir()

	t.Run("source with an edited line", func(t *testing.T) {
		edited := append([]string{}, lines...)
		edited[2] = strings.Replace(edited[2], `"id":"penguin"`, `"id":"alfred"`, 1)
		fx.write(t, edited...)
		before, err := os.ReadFile(fx.path)
		must(t, err)
		dest := filepath.Join(dir, "a", "j")
		err = MoveFile(fx.path, dest, fx.keys)
		if !errors.Is(err, ErrMoveRefused) || !errors.Is(err, ErrCorrupt) {
			t.Fatalf("err = %v, want ErrMoveRefused wrapping ErrCorrupt", err)
		}
		if after, _ := os.ReadFile(fx.path); !bytes.Equal(before, after) {
			t.Error("the refused move changed the source")
		}
		if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the refused move created the destination: %v", err)
		}
	})

	t.Run("destination holds another journal", func(t *testing.T) {
		fx.write(t, lines...)
		other := newFixture(t)
		other.keys = fx.keys
		oj := other.open(t)
		other.commitRevoke(t, oj, "bane")
		must(t, oj.Close())
		dest := filepath.Join(dir, "b", "j")
		must(t, os.MkdirAll(filepath.Dir(dest), 0o700))
		foreign, err := os.ReadFile(other.path)
		must(t, err)
		must(t, os.WriteFile(dest, foreign, 0o600))
		if err := MoveFile(fx.path, dest, fx.keys); !errors.Is(err, ErrMoveRefused) {
			t.Fatalf("err = %v, want ErrMoveRefused", err)
		}
		if after, _ := os.ReadFile(dest); !bytes.Equal(after, foreign) {
			t.Error("the destination was overwritten")
		}
	})

	t.Run("missing source", func(t *testing.T) {
		missing := filepath.Join(dir, "never", "j")
		must(t, os.MkdirAll(filepath.Dir(missing), 0o700))
		if err := MoveFile(missing, filepath.Join(dir, "c", "j"), fx.keys); err != nil {
			t.Fatalf("err = %v, want nil: nothing to move", err)
		}
		if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the move created the missing source: %v", err)
		}
	})

	t.Run("same path and locked source", func(t *testing.T) {
		fx.write(t, lines...)
		if err := MoveFile(fx.path, fx.path, fx.keys); err != nil {
			t.Errorf("a move onto itself: %v", err)
		}
		j := fx.open(t) // holds the lock: a move would copy a file a writer may still extend
		defer j.Close()
		if err := MoveFile(fx.path, filepath.Join(dir, "d", "j"), fx.keys); !errors.Is(err, ErrMoveRefused) || !errors.Is(err, ErrLocked) {
			t.Errorf("a move of a locked journal: err = %v, want ErrMoveRefused wrapping ErrLocked", err)
		}
	})

	t.Run("torn tail is not moved", func(t *testing.T) {
		fx.write(t, lines...)
		f, err := os.OpenFile(fx.path, os.O_APPEND|os.O_WRONLY, 0)
		must(t, err)
		_, err = f.WriteString(`{"seq":4,"kind":"user.dis`)
		must(t, errors.Join(err, f.Close()))
		dest := filepath.Join(dir, "e", "j")
		must(t, MoveFile(fx.path, dest, fx.keys))
		got, err := os.ReadFile(dest)
		must(t, err)
		if want := strings.Join(lines, "\n") + "\n"; string(got) != want {
			t.Errorf("the moved journal carries the torn tail: %q", got)
		}
	})
}
