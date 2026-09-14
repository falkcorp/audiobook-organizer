// file: internal/metadata/book_file_hashes.go
// version: 1.0.0
// guid: 5a9c3e71-2d48-4b06-9f15-c7e0b8d4a2f6
// last-edited: 2026-09-13

package metadata

import (
	"path/filepath"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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
// is installed or no row owns the path. Exported for byte writers outside this
// package that know only a path (the server's movement-atom cleanup).
func BookFileHashOptions(path string) fileops.WriteTagsSafeOptions {
	store := bookFileHashStore()
	if store == nil {
		return fileops.WriteTagsSafeOptions{}
	}
	return fileops.HashOptionsForPath(store, path)
}

// safeWriteDepsFor is packageSafeWriteDeps with path's book_file row attached,
// for the tagger.WriteTagsSafe calls of the default (WASM) writer.
func safeWriteDepsFor(path string) tagger.SafeWriteDeps {
	deps := packageSafeWriteDeps
	o := BookFileHashOptions(path)
	deps.BookFileID, deps.HashStore = o.BookFileID, o.Store
	return deps
}

// writeMetadataViaCLIRecordingHashes runs the CLI (ffmpeg) writers, which
// rewrite the file outside fileops.WriteTagsSafe, and then records the same
// three hashes WriteTagsSafe would: the sampled digest of the bytes before and
// after, and the whole-file SHA-256 after. A hashing failure is logged and
// leaves that column as it was; the write itself has already succeeded.
func writeMetadataViaCLIRecordingHashes(filePath string, md map[string]any, config fileops.OperationConfig) error {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		abs = filePath
	}
	opts := BookFileHashOptions(abs)
	if opts.BookFileID == "" || opts.Store == nil {
		return writeMetadataViaCLI(filePath, md, config)
	}
	log := logger.New("metadata")

	pre, preErr := filehash.BookFileHash(abs)
	if preErr != nil {
		pre = ""
		log.Warn("pre-write identity hash not computed; original_file_hash left as is: path=%s error=%v",
			logger.SanitizeLogValue(abs), preErr)
	}
	if err := writeMetadataViaCLI(filePath, md, config); err != nil {
		return err
	}
	post, postErr := filehash.BookFileHash(abs)
	if postErr != nil {
		post = ""
		log.Warn("identity hash not computed after CLI write; file_hash left unchanged: path=%s error=%v",
			logger.SanitizeLogValue(abs), postErr)
	}
	full, _, fullErr := fileops.ComputeFileHashAndSize(abs)
	if fullErr != nil {
		full = ""
		log.Warn("post-write SHA-256 not computed after CLI write: path=%s error=%v",
			logger.SanitizeLogValue(abs), fullErr)
	}
	if uerr := opts.Store.UpdateBookFileHashes(opts.BookFileID, pre, full, post); uerr != nil {
		log.Warn("hash columns not updated after CLI write; the file was written: book_file_id=%s path=%s error=%v",
			logger.SanitizeLogValue(opts.BookFileID), logger.SanitizeLogValue(abs), uerr)
	}
	return nil
}
