// file: internal/bookfiles/first_audio_file.go
// version: 2.0.0
// guid: 5b0f8c3e-7a41-4d92-9e16-c2d7a8b34f05
// last-edited: 2026-10-10

// Package bookfiles holds read helpers over book_file rows. It sits outside
// package database on purpose: the helper needs internal/audioext, and
// importing that from internal/database would grow the SDK's internal
// dependency closure (tools/cmd/sdkguard ratchet).
package bookfiles

import (
	"github.com/falkcorp/audiobook-organizer/internal/audioext"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// FileLister is the one read FirstAudioFile needs. database.Store satisfies it.
type FileLister interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// BookFilesBatcher is the one read FirstAudioFiles needs. database.Store
// satisfies it, so no capability lookup is required.
type BookFilesBatcher interface {
	GetBookFilesForIDsCore(bookIDs []string) (map[string][]database.BookFileCore, error)
}

// firstAudioIndex returns the index of the first usable audio row of rows by
// (disc, track, path) -- the order GetBookFiles sorts by -- or -1 when none
// qualifies. A row is usable when it is not Missing, has a non-empty path and
// that path's extension is in exts.
func firstAudioIndex[T any](rows []T, exts audioext.Set, key func(T) (disc, track int, path string, missing bool)) int {
	best := -1
	var bd, bt int
	var bp string
	for i, r := range rows {
		d, t, p, missing := key(r)
		if missing || p == "" || !exts.MatchPath(p) {
			continue
		}
		if best >= 0 {
			if d > bd || (d == bd && (t > bt || (t == bt && p >= bp))) {
				continue
			}
		}
		best, bd, bt, bp = i, d, t, p
	}
	return best
}

// FirstAudioFile returns the first present audio file of the book by
// (disc, track, path). Track 0 (untagged) sorts before track 1, matching
// GetBookFiles. ok=false means "no usable file row": the book has no rows, or
// every row is Missing, has an empty path or is not audio. It is never an
// error and NEVER a fallback to Book.FilePath, which is stale for many books;
// the book_file rows are the truth.
//
// configured is the raw supported-extensions list (config.AppConfig
// .SupportedExtensions). It is resolved here with audioext.Resolve, so a nil
// or empty list falls back to the default set instead of making every book
// look like it has no audio.
//
// Ties on identical (disc, track, path) may pick a different row than
// GetBookFiles()[0], because GetBookFiles uses the unstable sort.Slice.
func FirstAudioFile(store FileLister, bookID string, configured []string) (f database.BookFile, ok bool, err error) {
	exts := audioext.Resolve(configured)
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		return database.BookFile{}, false, err
	}
	i := firstAudioIndex(files, exts, func(f database.BookFile) (int, int, string, bool) {
		return f.DiscNumber, f.TrackNumber, f.FilePath, f.Missing
	})
	if i < 0 {
		return database.BookFile{}, false, nil
	}
	return files[i], true, nil
}

// FirstAudioFiles is the batch form of FirstAudioFile for list pages: one
// store call for N books. The result holds an entry only for books that have a
// usable audio file; absent means ok=false in the single form. It applies the
// same rules and ordering as FirstAudioFile (including track 0 sorting before
// track 1, the identical-key tie caveat, and resolving configured with
// audioext.Resolve) and never reads Book.FilePath. On the memdb path a missing
// map entry can also mean memdb lost rows, so callers that act on "no file"
// should confirm with the single form.
func FirstAudioFiles(store BookFilesBatcher, bookIDs []string, configured []string) (map[string]database.BookFileCore, error) {
	exts := audioext.Resolve(configured)
	byBook, err := store.GetBookFilesForIDsCore(bookIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]database.BookFileCore, len(byBook))
	for _, id := range bookIDs {
		rows := byBook[id]
		i := firstAudioIndex(rows, exts, func(f database.BookFileCore) (int, int, string, bool) {
			return f.DiscNumber, f.TrackNumber, f.FilePath, f.Missing
		})
		if i >= 0 {
			out[id] = rows[i]
		}
	}
	return out, nil
}
