// file: internal/scanner/folder_evidence.go
// version: 1.2.0
// guid: b4e4e76d-6d71-4a99-91b9-7b9725a1268a
// last-edited: 2026-10-06
//
// Person and series evidence for the folder parse.

package scanner

import (
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/foldernames"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// folderEvidenceTTL is how long one foldernames.Snapshot (the author and
// series lists the folder parse checks) is reused before it is re-read: a
// series created mid-scan is picked up on the next refresh, and a
// whole-library scan reads the lists a handful of times, not once per book.
const folderEvidenceTTL = 10 * time.Minute

var folderEvidenceCache struct {
	mu    sync.Mutex
	store scannerStore
	at    time.Time
	snap  *foldernames.Snapshot
	// loading is non-nil while one caller reads the lists, and closed when
	// it is done.
	loading chan struct{}
}

// FolderNameEvidence returns the evidence the folder parse
// (metadata.ExtractMetadataFromFolderWith) decides an author-or-series
// segment with, read from the scanner's store through foldernames: the
// authority person lists, the library's author rows and its series names
// with author-junk series rows filtered out. With no store, or before the
// first snapshot loads, it is empty and the parse falls back to the
// person-name shape. The importer uses it too.
func FolderNameEvidence() metadata.NameEvidence {
	store := getStore()
	if store == nil {
		return metadata.NameEvidence{}
	}
	return folderSnapshot(store).Evidence()
}

// folderSnapshot returns the cached snapshot for store, reloading it when it
// is older than folderEvidenceTTL. One caller reads the lists, with the mutex
// released so the parse of every other book never stalls behind it: while it
// reloads, the others keep the previous snapshot, and when there is none yet
// (the first books of a scan, all arriving at once) they wait for that one
// load rather than each reading the ~50k series and ~15k authors itself. A
// failed load is logged and keeps the previous snapshot; with none, the
// parse has no library evidence and falls back to the person-name shape.
func folderSnapshot(store scannerStore) *foldernames.Snapshot {
	c := &folderEvidenceCache
	c.mu.Lock()
	for {
		sameStore := c.store == store && c.snap != nil
		if sameStore && (time.Since(c.at) < folderEvidenceTTL || c.loading != nil) {
			snap := c.snap
			c.mu.Unlock()
			return snap
		}
		if c.loading == nil {
			break
		}
		wait := c.loading
		c.mu.Unlock()
		<-wait
		c.mu.Lock()
	}
	var prev *foldernames.Snapshot
	if c.store == store {
		prev = c.snap
	}
	done := make(chan struct{})
	c.loading = done
	c.mu.Unlock()

	snap, err := foldernames.Load(store)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.loading = nil
	close(done)
	if err != nil {
		defaultLog.Warn("folder-name evidence: reading the author and series lists failed (%v); "+
			"the folder parse keeps the previous lists (none: person-name shape only)", err)
		return prev
	}
	c.store, c.at, c.snap = store, time.Now(), snap
	return snap
}
