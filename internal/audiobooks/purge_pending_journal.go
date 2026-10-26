// file: internal/audiobooks/purge_pending_journal.go
// version: 1.4.0
// guid: 5b2d8e60-1c47-4a93-b0f5-9e3a7d1c4826
// last-edited: 2026-10-25

package audiobooks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pendingFilePrefix keys the journal of file removals a purge pass has decided
// on and not yet carried out: one record per purged book, keyed by the book's
// id, naming the file (or empty directory) its purge handed to the pass's
// deferred removal (pendingFiles, removeDeferredFiles).
//
// Why it exists. A pass with delete_files does not remove a file the moment its
// book is purged: it waits until every book is decided, because the file may
// also be a kept book's (removeDeferredFiles). But the purge deletes the row and
// the tombstone straight away, so for as long as the pass runs nothing on disk
// or in the store says "this book's file is still to be removed". A crash, an
// OOM or a deploy mid-pass would leave the files of every book already purged on
// disk with no record naming them, and the next library scan could import them
// back as new books. The record is written before the tombstone goes
// (purgeFinish), removed once the path is decided (removeDeferredFiles), and
// replayed by the next pass (replayPendingFileRemovals).
//
// Why not keep the tombstone instead. sweep.SweepTombstones os.Remove()s a
// tombstone's FilePath with no library-root, protected-path or still-referenced
// check (internal/sweep/sweeper.go), so a tombstone left standing would be a
// worse hazard than an orphaned file. The journal is replayed only through the
// same checks removeDeferredFiles runs.
const pendingFilePrefix = "purge_pending_file:"

// pendingFileRecord is one journal entry. Identity is the file as the pass saw
// it when it decided to remove it: a replay can come days later, and removes
// only a file that is still that one (fileIdentity).
type pendingFileRecord struct {
	BookID     string       `json:"book_id"`
	Path       string       `json:"path"`
	IsDir      bool         `json:"is_dir,omitempty"`
	Identity   fileIdentity `json:"identity"`
	RecordedAt time.Time    `json:"recorded_at"`
	// Attempts counts the passes that kept the record because its path cannot
	// be resolved (ELOOP, ENAMETOOLONG); at maxUnresolvableAttempts it is
	// cleared with an error line.
	Attempts int `json:"attempts,omitempty"`
}

// earlierPassLabel marks the error lines of a replayed removal: they are about
// books an earlier pass purged, not this one.
const earlierPassLabel = "from an earlier pass: "

// pendingFileKV is the raw key surface the journal lives in, asserted on the
// store rather than widening audiobookStore (the way revert_settle.go does it).
type pendingFileKV interface {
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
	DeleteRawBatch(keys []string) error
	ScanPrefixPage(prefix, after string, limit int) (pairs []database.KVPair, next string, err error)
}

// pendingFileScanPage is how many journal records one scan page reads.
const pendingFileScanPage = 1000

func (svc *AudiobookService) pendingFileKV() (pendingFileKV, error) {
	kv, ok := any(svc.store).(pendingFileKV)
	if !ok {
		return nil, fmt.Errorf("the store has no raw key-value surface to keep the journal of pending file removals in")
	}
	return kv, nil
}

// journalPendingFile durably records that bookID's purge decided to remove the
// file v names. It must return before the book's tombstone is deleted.
func (svc *AudiobookService) journalPendingFile(bookID string, v purgeFileVerdict) error {
	kv, err := svc.pendingFileKV()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(pendingFileRecord{BookID: bookID, Path: v.remove, IsDir: v.isDir, Identity: v.id, RecordedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("encode pending file removal: %w", err)
	}
	if err := kv.SetRaw(pendingFilePrefix+bookID, raw); err != nil {
		return fmt.Errorf("record pending file removal: %w", err)
	}
	return nil
}

// recordUnresolvableAttempt counts one more attempt on the journal records of f
// (a path that cannot be resolved), keeping each record exactly as written
// otherwise: its own identity and write time. The records of one path can
// disagree about the file (f.idConflict: books decided at different moments saw
// different files, and the path is then never removed); rewriting them all from
// f's single merged identity would make them agree, and let the path be removed
// on a match with the first one. A record that is not there (never journaled, or
// cleared meanwhile) is left unwritten: this never creates one.
func (svc *AudiobookService) recordUnresolvableAttempt(f *pendingFile) error {
	kv, err := svc.pendingFileKV()
	if err != nil {
		return err
	}
	for _, bookID := range f.bookIDs {
		key := pendingFilePrefix + bookID
		raw, err := kv.GetRaw(key)
		if err != nil {
			return fmt.Errorf("read pending file removal: %w", err)
		}
		if raw == nil {
			continue
		}
		var rec pendingFileRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue // unreadable: the next load drops it and says so
		}
		rec.Attempts = f.attempts + 1
		out, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("encode pending file removal: %w", err)
		}
		if err := kv.SetRaw(key, out); err != nil {
			return fmt.Errorf("record pending file removal: %w", err)
		}
	}
	return nil
}

// forgetPendingFiles removes the journal records of bookIDs in one durable
// write. Records that do not exist are not an error (a store with no journal
// has none to remove, and a book whose record failed to write has none).
func (svc *AudiobookService) forgetPendingFiles(bookIDs []string) error {
	if len(bookIDs) == 0 {
		return nil
	}
	kv, err := svc.pendingFileKV()
	if err != nil {
		// Nothing was ever written to a store that cannot hold the journal.
		return nil
	}
	keys := make([]string, len(bookIDs))
	for i, id := range bookIDs {
		keys[i] = pendingFilePrefix + id
	}
	return kv.DeleteRawBatch(keys)
}

// readPendingFiles reads the whole journal into a pendingFiles, grouping the
// records by path as a pass does, and changes nothing. A record that cannot be
// decoded names no path and can never be acted on: its key is returned in
// corrupt, and a line for it in errs.
func (svc *AudiobookService) readPendingFiles() (files *pendingFiles, corrupt, errs []string, err error) {
	kv, kvErr := svc.pendingFileKV()
	if kvErr != nil {
		return &pendingFiles{}, nil, nil, nil
	}
	files = &pendingFiles{}
	after := ""
	for {
		pairs, next, scanErr := kv.ScanPrefixPage(pendingFilePrefix, after, pendingFileScanPage)
		if scanErr != nil {
			return nil, nil, nil, fmt.Errorf("read the journal of pending file removals: %w", scanErr)
		}
		for _, p := range pairs {
			var rec pendingFileRecord
			if jsonErr := json.Unmarshal(p.Value, &rec); jsonErr != nil || rec.Path == "" || rec.BookID == "" {
				corrupt = append(corrupt, p.Key)
				errs = append(errs, fmt.Sprintf("dropped the unreadable pending file removal record %s", p.Key))
				continue
			}
			files.add(rec.BookID, purgeFileVerdict{remove: rec.Path, isDir: rec.IsDir, id: rec.Identity})
			files.markJournaled(rec.Path, rec.Attempts, rec.RecordedAt)
		}
		if next == "" {
			break
		}
		after = next
	}
	return files, corrupt, errs, nil
}

// countPendingFileRemovals is how many files (paths) an earlier pass left
// waiting in the journal. It reads and changes nothing, so a preview can show
// what a purge with delete_files would also remove.
func (svc *AudiobookService) countPendingFileRemovals() (int, error) {
	files, _, _, err := svc.readPendingFiles()
	if err != nil {
		return 0, err
	}
	return len(files.sorted()), nil
}

// PendingFileRemovals is countPendingFileRemovals for callers outside the
// package (the trash listing).
func (svc *AudiobookService) PendingFileRemovals() (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	return svc.countPendingFileRemovals()
}

// loadPendingFiles is readPendingFiles for a pass that acts on the journal: the
// records that cannot be decoded are dropped (and reported).
func (svc *AudiobookService) loadPendingFiles() (files *pendingFiles, errs []string, err error) {
	files, corrupt, errs, err := svc.readPendingFiles()
	if err != nil {
		return nil, nil, err
	}
	kv, kvErr := svc.pendingFileKV()
	if kvErr != nil {
		return files, errs, nil
	}
	if len(corrupt) > 0 {
		if delErr := kv.DeleteRawBatch(corrupt); delErr != nil {
			errs = append(errs, fmt.Sprintf("could not drop %d unreadable pending file removal record(s): %v", len(corrupt), delErr))
		}
	}
	return files, errs, nil
}

// replayPendingFileRemovals carries out the removals an earlier pass decided on
// and did not finish (it crashed, or was stopped by a restart). Each goes
// through exactly the checks removeDeferredFiles runs for a pass in progress:
// protected path, library root, any book_file row or live book, any book row at
// all (trashed ones included), still there, and still the same file
// (fileIdentity). There is no preview to hold it to, and none is needed: the
// journal holds only paths the pass's own preview had listed (purgeFinish).
//
// It is also run once at boot (ReplayPendingFileRemovalsOnBoot), so records a
// crash left do not wait for a purge to be asked for.
//
// RunPurge calls it only for a pass that deletes files: a purge the owner ran
// with "also delete files" off never removes a file, whatever an earlier pass
// left. A resumed purge op carries its saved delete_files, so a crashed pass's
// resume still replays. It runs before the preview refusals
// (ErrPurgePreviewUnknown, ErrPurgePreviewRequired) and does not depend on a
// preview.
//
// It stops between paths when ctx is done (a shutdown, a cancelled op): the
// paths it did not reach keep their records, and the next replay decides them.
//
// It reports how many things it removed and the error lines, each labelled as
// being about an earlier pass (earlierPassLabel).
func (svc *AudiobookService) replayPendingFileRemovals(ctx context.Context) (removed int, errs []string) {
	// A replay changes nothing while the library root is not ready: every "no
	// file here" it would read is a missing mount, and it would clear the record
	// of a file that is still on the real disk (libraryRootReady). The records,
	// readable or not, are all kept.
	if rootErr := svc.libraryRootReady(config.Snapshot().RootDir); rootErr != nil {
		return 0, labelEarlierPass([]string{fmt.Sprintf("nothing replayed, every record kept: %v", rootErr)})
	}
	// One removal run at a time (replayMu: held for the whole run, never by a
	// writer of the journal). Only the READ of the records is exclusive of the
	// writers (pendingMu), between a record's journal write and its row delete
	// (purgeDeleteRow): a replay that read such a record would find the book's
	// row still there, take that for "a book still has this file" and clear the
	// record of a book that is about to be purged. A record the read returns
	// belongs to a book already deleted, and a writer that starts afterwards
	// writes a record the read did not return, so nothing after the read needs
	// the lock, and a purge holding the merge lock never waits on the stats and
	// unlinks below.
	svc.replayMu.Lock()
	defer svc.replayMu.Unlock()
	svc.pendingMu.Lock()
	files, loadErrs, err := svc.loadPendingFiles()
	svc.pendingMu.Unlock()
	errs = append(errs, loadErrs...)
	if err != nil {
		errs = append(errs, err.Error())
		return 0, labelEarlierPass(errs)
	}
	n, e := svc.removeDeferredFiles(ctx, files, nil)
	return n, labelEarlierPass(append(errs, e...))
}

// labelEarlierPass prefixes each line with earlierPassLabel
// ("from an earlier pass: <id>: book purged, file kept: ..."), so a line about a
// book an earlier pass purged is not read as this pass's.
func labelEarlierPass(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = earlierPassLabel + l
	}
	return out
}
