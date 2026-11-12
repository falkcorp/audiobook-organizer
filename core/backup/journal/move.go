// file: core/backup/journal/move.go
// version: 1.1.0
// guid: 5a2c7e19-0b3d-4f86-a4d1-9e7c6b1f3a28
// last-edited: 2026-11-11

package journal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrMoveRefused reports a journal move that did not happen: the source does
// not verify, or the destination holds another journal. The old file is
// untouched; the boot reports revocation_journal_broken and keeps using it
// (05-backup §5B.3.6 row "Where").
var ErrMoveRefused = errors.New("journal: the journal move was refused")

// MoveFile moves the journal at from to to when the effective backup
// directory changed (05-backup §5B.3.6 row "Where"): it holds the source's
// lock, verifies the whole source chain with keys, copies it to a temp file
// in the destination directory (0600), fsyncs, verifies the copy byte for
// byte, renames it into place and fsyncs the directory. The old file is left
// where it is: the caller updates backup.location.json to the new path first
// and only then removes it, so a crash in between leaves the record pointing
// at a file that still exists, and the next boot's MoveFile finds the
// destination already holding the verified copy and returns nil.
//
// A source that does not verify is ErrMoveRefused (wrapping ErrCorrupt), and
// nothing is written. A missing source is nil: there is nothing to move, and
// the destination gets a new journal when the component binds it. A
// destination that exists is accepted only when it verifies and holds the
// source's last line unchanged; any other file there is ErrMoveRefused and is
// never overwritten. Neither end is ever a link: a source that is not a regular
// file, or a destination that exists and is not one, is ErrMoveRefused, and the
// temp file is created fresh (a leftover of an earlier crash, or a name someone
// planted on the share, is removed first and never written through).
func MoveFile(from, to string, keys Keys) (err error) {
	if from == to {
		return nil
	}
	// openJournal creates a missing file, which a move must not do.
	if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	f, data, err := openJournal(from)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMoveRefused, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }() // releases the lock

	src := ReadChain(data, keys)
	if src.Break != nil {
		return fmt.Errorf("%w: %s does not verify: %w: %w", ErrMoveRefused, from, ErrCorrupt, src.Break)
	}
	data = data[:src.ValidLen] // an incomplete last line never returned, so it is not moved
	if len(data) == 0 {
		return nil // an empty journal holds nothing to move
	}
	switch fi, err := os.Lstat(to); {
	case err == nil && !fi.Mode().IsRegular():
		return fmt.Errorf("%w: %w: %s is a %s", ErrMoveRefused, ErrNotRegular, to, fileKind(fi.Mode()))
	case err == nil:
		existing, err := os.ReadFile(to)
		if err != nil {
			return fmt.Errorf("%w: read %s: %w", ErrMoveRefused, to, err)
		}
		if !holdsMoved(ReadChain(existing, keys), src) {
			return fmt.Errorf("%w: %s already holds another journal", ErrMoveRefused, to)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: stat %s: %w", ErrMoveRefused, to, err)
	}

	dir := filepath.Dir(to)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrMoveRefused, err)
	}
	tmp := to + ".moving"
	// Whatever is at the temp name is ours to replace, never to write through: a
	// regular file is a crash's leftover, anything else a name planted on the
	// share (removing a link removes the link). The create below is exclusive.
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: clear %s: %w", ErrMoveRefused, tmp, err)
	}
	if err := writeDurable(tmp, data); err != nil {
		return errors.Join(fmt.Errorf("%w: %w", ErrMoveRefused, err), os.Remove(tmp))
	}
	copied, err := os.ReadFile(tmp)
	if err != nil || !bytes.Equal(copied, data) {
		return errors.Join(fmt.Errorf("%w: the copy at %s does not match the source (%v)", ErrMoveRefused, tmp, err), os.Remove(tmp))
	}
	if c := ReadChain(copied, keys); c.Break != nil || c.IntactEnd() != src.IntactEnd() {
		return errors.Join(fmt.Errorf("%w: the copy at %s does not verify", ErrMoveRefused, tmp), os.Remove(tmp))
	}
	if err := os.Rename(tmp, to); err != nil {
		return errors.Join(fmt.Errorf("%w: %w", ErrMoveRefused, err), os.Remove(tmp))
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("%w: %w", ErrMoveRefused, err)
	}
	return nil
}

// holdsMoved reports whether dst is the verified copy of src, or src extended:
// the same header and the source's last line at the same position.
func holdsMoved(dst, src *Chain) bool {
	if dst.Break != nil || !dst.HasHeader || dst.Header.MAC != src.Header.MAC || dst.IntactEnd() < src.IntactEnd() {
		return false
	}
	if len(src.Lines) == 0 {
		return true
	}
	mac, ok := dst.MACAt(src.IntactEnd())
	return ok && macEqual(mac, src.PrevMAC())
}

// writeDurable creates path (0600, exclusively: it fails when anything, a
// symbolic link included, is already there), writes data and fsyncs.
func writeDurable(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|openNoFollow, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
