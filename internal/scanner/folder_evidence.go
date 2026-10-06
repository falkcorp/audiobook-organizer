// file: internal/scanner/folder_evidence.go
// version: 1.1.0
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
	mu      sync.Mutex
	store   scannerStore
	at      time.Time
	snap    *foldernames.Snapshot
	loading bool
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
// is older than folderEvidenceTTL. The lists are read with the mutex
// released, so a reload never stalls the parse of every other book: while
// one caller reloads, the others keep the previous snapshot. A failed load
// keeps the previous snapshot (nil when there is none).
func folderSnapshot(store scannerStore) *foldernames.Snapshot {
	c := &folderEvidenceCache
	c.mu.Lock()
	sameStore := c.store == store
	if sameStore && c.snap != nil && (time.Since(c.at) < folderEvidenceTTL || c.loading) {
		snap := c.snap
		c.mu.Unlock()
		return snap
	}
	var prev *foldernames.Snapshot
	if sameStore {
		prev = c.snap
	}
	c.loading = true
	c.mu.Unlock()

	snap, err := foldernames.Load(store)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.loading = false
	if err != nil {
		return prev
	}
	c.store, c.at, c.snap = store, time.Now(), snap
	return snap
}
