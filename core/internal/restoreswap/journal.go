// file: core/internal/restoreswap/journal.go
// version: 1.0.4
// guid: d009d8c5-8883-4296-8e4d-d30871926413
// last-edited: 2026-11-08

package restoreswap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The journal steps (05-backup §5B.3.4, 05-storage §5S.3.15, §5S.3.16
// machine 1). Restore kinds: staged, rejected, rechecked, previous_moved,
// staged_moved, sidecars, swapped, opened, marker_written, applied, failed,
// swapped_back. KindReset: staged, set_aside_started, moved, set_aside,
// applied, hook_done, rejected.
const (
	StepStaged          = "staged"
	StepRejected        = "rejected"
	StepRechecked       = "rechecked"
	StepPreviousMoved   = "previous_moved"
	StepStagedMoved     = "staged_moved"
	StepSidecars        = "sidecars"
	StepSwapped         = "swapped"
	StepOpened          = "opened"
	StepMarkerWritten   = "marker_written"
	StepApplied         = "applied"
	StepFailed          = "failed"
	StepSwappedBack     = "swapped_back"
	StepSetAsideStarted = "set_aside_started"
	StepMoved           = "moved"
	StepSetAside        = "set_aside"
	StepHookDone        = "hook_done"
)

// ErrJournalCorrupt: a journal line before the last does not decode. The
// journal is the swap's only record of what moved, so boot halts naming it
// rather than guess (a torn LAST line was never acknowledged and is
// dropped).
var ErrJournalCorrupt = errors.New("restoreswap: swap journal is corrupt")

// ReadJournal reads restore/<id>/journal.json through fs: one Record per
// line (decision D2: JSON Lines, appended and fsynced). A final line that
// does not end in a newline or does not decode is a torn append that was
// never acknowledged and is dropped; an undecodable line before it is
// ErrJournalCorrupt. A missing journal is no records.
func ReadJournal(fs vfs.FS, stateDir, id string) ([]restorejournal.Record, error) {
	recs, _, _, err := readJournal(fs, journalPath(stateDir, id))
	return recs, err
}

func journalPath(stateDir, id string) string {
	return filepath.Join(restorejournal.JournalDir(stateDir, id), restorejournal.JournalName)
}

// readJournal returns the records, the bytes of every acknowledged line
// (the file up to just past the last one, where the next append goes) and
// whether a torn tail followed them and was dropped.
func readJournal(fs vfs.FS, path string) (recs []restorejournal.Record, acked []byte, torn bool, err error) {
	f, err := fs.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, false, nil
		}
		return nil, nil, false, fmt.Errorf("restoreswap: open journal %s: %w", path, err)
	}
	all, err := io.ReadAll(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("restoreswap: read journal %s: %w", path, err)
	}
	b, end := all, 0
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break // torn: never acknowledged
		}
		line := b[:i]
		var r restorejournal.Record
		if err := json.Unmarshal(line, &r); err != nil {
			if bytes.IndexByte(b[i+1:], '\n') < 0 {
				break // the last line, torn mid-write
			}
			return nil, nil, false, fmt.Errorf("%w: %s line %d: %v", ErrJournalCorrupt, path, len(recs)+1, err)
		}
		recs = append(recs, r)
		end += i + 1
		b = b[i+1:]
	}
	return recs, all[:end], end < len(all), nil
}

// lastStep is the journal's last step, "" when empty.
func lastStep(recs []restorejournal.Record) string {
	if len(recs) == 0 {
		return ""
	}
	return recs[len(recs)-1].Step
}

// appendRec appends one record to restore/<id>/journal.json and fsyncs it,
// labelled swap.journal.<step>; the first append also fsyncs the journal's
// directory so the file itself is durable. It writes just past the last
// acknowledged line; a journal with a torn tail is rewritten instead
// (rewriteJournal), so the file never holds bytes past its last line but
// those of the one append in progress.
func (s *Swapper) appendRec(ctx context.Context, id string, r restorejournal.Record) error {
	exit := bootseam.Region(ctx, "swap.journal."+r.Step)
	defer exit()
	if r.At.IsZero() {
		r.At = s.now().UTC()
	}
	if r.Actor == "" {
		r.Actor = s.o.Actor
	}
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("restoreswap: encode journal record %s: %w", r.Step, err)
	}
	line = append(line, '\n')
	path := journalPath(s.o.StateDir, id)
	_, acked, torn, err := readJournal(s.fs, path)
	if err != nil {
		return err
	}
	if torn {
		return s.rewriteJournal(path, acked, line, r.Step)
	}
	end := int64(len(acked))
	_, statErr := s.fs.Stat(path)
	fresh := errors.Is(statErr, os.ErrNotExist)
	var f vfs.File
	if fresh {
		f, err = s.fs.Create(path, vfs.WriteCategoryUnspecified)
		if err == nil {
			// SEC-9: 0600 before the first byte; a journal that cannot be
			// made private is removed, so the next append creates it again.
			if cherr := restorejournal.ChmodFS(s.fs, path, privateFile); cherr != nil {
				return errors.Join(fmt.Errorf("restoreswap: create journal %s: %w", path, cherr), f.Close(), s.fs.Remove(path))
			}
		}
	} else {
		f, err = s.fs.OpenReadWrite(path, vfs.WriteCategoryUnspecified)
	}
	if err != nil {
		return fmt.Errorf("restoreswap: open journal %s: %w", path, err)
	}
	_, werr := f.WriteAt(line, end)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("restoreswap: append %s to journal %s: %w", r.Step, path, err)
	}
	if fresh {
		return syncDir(s.fs, filepath.Dir(path))
	}
	return nil
}

// durableJournal is id's journal for a boot about to act on it, made
// durable first (R1-21 review M6, found by the crash matrix's nested
// faults): a record an earlier boot appended and failed to fsync is visible
// here and not durable, and an action it allows (the slot rename after
// rejected, the in-flight file's removal after a terminal step) would
// outlive it in a crash. The journal is rewritten (rewriteJournal), not
// just fsynced again: a kernel that marks the pages clean after a failed
// writeback reports the second fsync done without writing them. A journal
// file with no record has its directory synced, so the file found is
// durable before the next append writes into it in place. Labelled
// swap.journal.resync.
func (s *Swapper) durableJournal(ctx context.Context, id string) ([]restorejournal.Record, error) {
	path := journalPath(s.o.StateDir, id)
	recs, acked, _, err := readJournal(s.fs, path)
	if err != nil {
		return nil, err
	}
	found, err := exists(s.fs, path)
	if err != nil || !found {
		return recs, err
	}
	return recs, bootseam.Labelled(bootseam.From(ctx), "swap.journal.resync", func() error {
		if len(recs) == 0 {
			return syncDir(s.fs, filepath.Dir(path))
		}
		return s.rewriteJournal(path, acked, nil, "resync")
	})
}

// rewriteJournal replaces a journal whose torn tail readJournal dropped
// with its acknowledged lines and line, through .journal.json.tmp renamed
// over it and the directory synced (R1-21 review m6). Writing line over the
// tail in place would leave the tail's stale bytes past a shorter line, and
// a later torn append could join them into an undecodable line before the
// last: ErrJournalCorrupt, which halts every boot. vfs.File has no truncate.
// A kill before the rename leaves the journal as it was, the record not
// acknowledged, and a temp the next rewrite overwrites. A temp that cannot
// be made 0600 (SEC-9) is removed with nothing written to it.
func (s *Swapper) rewriteJournal(path string, acked, line []byte, step string) error {
	tmp := filepath.Join(filepath.Dir(path), "."+restorejournal.JournalName+".tmp")
	f, err := s.fs.Create(tmp, vfs.WriteCategoryUnspecified)
	if err != nil {
		return fmt.Errorf("restoreswap: rewrite journal %s: %w", path, err)
	}
	if err := restorejournal.ChmodFS(s.fs, tmp, privateFile); err != nil {
		return errors.Join(fmt.Errorf("restoreswap: rewrite journal %s for %s: %w", path, step, err), f.Close(), s.fs.Remove(tmp))
	}
	_, werr := f.Write(append(append([]byte{}, acked...), line...))
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("restoreswap: rewrite journal %s for %s: %w", path, step, err)
	}
	if err := s.fs.Rename(tmp, path); err != nil {
		return fmt.Errorf("restoreswap: rewrite journal %s for %s: %w", path, step, err)
	}
	return syncDir(s.fs, filepath.Dir(path))
}
