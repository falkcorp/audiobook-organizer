// file: core/internal/restoreswap/journal.go
// version: 1.0.0
// guid: d009d8c5-8883-4296-8e4d-d30871926413
// last-edited: 2026-11-07

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
	recs, _, err := readJournal(fs, journalPath(stateDir, id))
	return recs, err
}

func journalPath(stateDir, id string) string {
	return filepath.Join(restorejournal.JournalDir(stateDir, id), restorejournal.JournalName)
}

// readJournal returns the records and the byte offset just past the last
// acknowledged line, where the next append writes (over any torn tail).
func readJournal(fs vfs.FS, path string) ([]restorejournal.Record, int64, error) {
	f, err := fs.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("restoreswap: open journal %s: %w", path, err)
	}
	b, err := io.ReadAll(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, 0, fmt.Errorf("restoreswap: read journal %s: %w", path, err)
	}
	var recs []restorejournal.Record
	var end int64
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
			return nil, 0, fmt.Errorf("%w: %s line %d: %v", ErrJournalCorrupt, path, len(recs)+1, err)
		}
		recs = append(recs, r)
		end += int64(i + 1)
		b = b[i+1:]
	}
	return recs, end, nil
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
// directory so the file itself is durable. It writes at the end of the last
// acknowledged line, over any torn tail.
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
	_, end, err := readJournal(s.fs, path)
	if err != nil {
		return err
	}
	_, statErr := s.fs.Stat(path)
	fresh := errors.Is(statErr, os.ErrNotExist)
	var f vfs.File
	if fresh {
		f, err = s.fs.Create(path, vfs.WriteCategoryUnspecified)
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
