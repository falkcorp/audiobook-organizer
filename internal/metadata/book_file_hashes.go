// file: internal/metadata/book_file_hashes.go
// version: 1.2.0
// guid: 5a9c3e71-2d48-4b06-9f15-c7e0b8d4a2f6
// last-edited: 2026-09-13

package metadata

import (
	"path/filepath"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// Every package-level write (WriteMetadataToFile, WriteSingleTag) rewrites the
// audio file, so the file's book_file row must learn the new hash or the next
// rescan treats the file as replaced. The callers know a book or a path, not a
// book_file ID, so the row is looked up by path through this store, installed
// once at server startup like packageSafeWriteDeps. Nil disables recording.
var (
	hashStoreMu      sync.RWMutex
	packageHashStore fileops.BookFileHashRecorder
)

// SetBookFileHashStore installs the store that package-level writes record
// file hashes through. Pass nil to disable (tests restore it this way).
func SetBookFileHashStore(store fileops.BookFileHashRecorder) {
	hashStoreMu.Lock()
	packageHashStore = store
	hashStoreMu.Unlock()
}

func bookFileHashStore() fileops.BookFileHashRecorder {
	hashStoreMu.RLock()
	defer hashStoreMu.RUnlock()
	return packageHashStore
}

// BookFileHashOptions returns the fileops.WriteTagsSafe options that record a
// write's hashes on the book_file row at path, or zero options when no store
// is installed or no row owns the path. It serves the writers that go through
// fileops directly rather than tagger: the native (cgo) taglib writer, which
// passes the already-resolved path, and the ffmpeg fallback. Tagger writes use
// WithBookFileHashes instead, so the row is found after any redirect.
func BookFileHashOptions(path string) fileops.WriteTagsSafeOptions {
	store := bookFileHashStore()
	if store == nil {
		return fileops.WriteTagsSafeOptions{}
	}
	return fileops.HashOptionsForPath(store, path)
}

// WithBookFileHashes returns deps with the installed hash store attached, so
// a tagger write records the new hashes on the book_file row of the file it
// actually wrote (tagger looks the row up after resolving any protected-path
// redirect). deps is returned unchanged when no store is installed. Exported
// for tagger writers outside this package (the server's movement-atom cleanup).
func WithBookFileHashes(deps tagger.SafeWriteDeps) tagger.SafeWriteDeps {
	if store := bookFileHashStore(); store != nil {
		deps.HashStore = store
	}
	return deps
}

// writeMetadataViaCLIRecordingHashes runs the CLI (ffmpeg) writers, which
// rewrite the file outside fileops.WriteTagsSafe, through fileops.RecordRewrite,
// which records the same three hashes WriteTagsSafe would on the file's
// book_file row.
func writeMetadataViaCLIRecordingHashes(filePath string, md map[string]any, config fileops.OperationConfig) error {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		abs = filePath
	}
	return fileops.RecordRewrite(bookFileHashStore(), abs, func() error {
		return writeMetadataViaCLI(filePath, md, config)
	})
}
