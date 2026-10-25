// file: internal/audiobooks/purge_pending_journal.go
// version: 1.1.0
// guid: 5b2d8e60-1c47-4a93-b0f5-9e3a7d1c4826
// last-edited: 2026-10-25

package audiobooks

import (
	"encoding/json"
	"fmt"
	"time"

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
}

// earlierPassLabel marks the error lines of a replayed removal: they are about
// books an earlier pass purged, not this one.
const earlierPassLabel = "from an earlier pass: "

// pendingFileKV is the raw key surface the journal lives in, asserted on the
// store rather than widening audiobookStore (the way revert_settle.go does it).
type pendingFileKV interface {
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
// RunPurge calls it only for a pass that deletes files: a purge the owner ran
// with "also delete files" off never removes a file, whatever an earlier pass
// left. A resumed purge op carries its saved delete_files, so a crashed pass's
// resume still replays. It runs before the preview refusals
// (ErrPurgePreviewUnknown, ErrPurgePreviewRequired) and does not depend on a
// preview.
//
// It reports how many things it removed and the error lines, each labelled as
// being about an earlier pass (earlierPassLabel).
func (svc *AudiobookService) replayPendingFileRemovals() (removed int, errs []string) {
	files, loadErrs, err := svc.loadPendingFiles()
	errs = append(errs, loadErrs...)
	if err != nil {
		errs = append(errs, err.Error())
		return 0, labelEarlierPass(errs)
	}
	n, e := svc.removeDeferredFiles(files, nil)
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
