// file: internal/scanner/folder_evidence.go
// version: 1.0.0
// guid: b4e4e76d-6d71-4a99-91b9-7b9725a1268a
// last-edited: 2026-10-06
//
// Person and series evidence for the folder parse.

package scanner

import (
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// folderSeriesTTL is how long the series-name set the folder parse checks is
// reused before it is re-read: a series created mid-scan is picked up on the
// next refresh, and a whole-library scan reads the series list a handful of
// times, not once per book.
const folderSeriesTTL = 10 * time.Minute

var folderSeriesCache struct {
	mu    sync.Mutex
	store scannerStore
	at    time.Time
	names map[string]bool
}

// FolderNameEvidence returns the evidence the folder parse
// (metadata.ExtractMetadataFromFolderWith) decides an author-or-series
// segment with, read from the scanner's store: the authority person lists
// (authority.Index: known authors and narrators), the library's author rows
// and its series names. With no store it is empty, and the parse falls back
// to the person-name shape. The importer uses it too.
func FolderNameEvidence() metadata.NameEvidence {
	store := getStore()
	if store == nil {
		return metadata.NameEvidence{}
	}
	idx := authority.NewIndex(store)
	return metadata.NameEvidence{
		IsKnownAuthor: func(name string) bool {
			if ok, err := idx.IsKnownPerson(name, authority.RoleAuthor); err == nil && ok {
				return true
			}
			ok, err := idx.IsKnownPerson(name, authority.RoleNarrator)
			return err == nil && ok
		},
		IsAuthorRow: func(name string) bool {
			a, err := store.GetAuthorByName(name)
			return err == nil && a != nil
		},
		IsKnownSeries: func(name string) bool {
			return folderSeriesNames(store)[personname.LettersKey(strings.TrimSpace(name))]
		},
	}
}

// folderSeriesNames returns the library's series names (personname.LettersKey
// folded), cached for folderSeriesTTL per store. A read error answers the
// last good set, or none.
func folderSeriesNames(store scannerStore) map[string]bool {
	c := &folderSeriesCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == store && c.names != nil && time.Since(c.at) < folderSeriesTTL {
		return c.names
	}
	all, err := store.GetAllSeries()
	if err != nil {
		if c.store == store && c.names != nil {
			return c.names
		}
		return nil
	}
	names := make(map[string]bool, len(all))
	for _, s := range all {
		if k := personname.LettersKey(strings.TrimSpace(s.Name)); k != "" {
			names[k] = true
		}
	}
	c.store, c.at, c.names = store, time.Now(), names
	return names
}
