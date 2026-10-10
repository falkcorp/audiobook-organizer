// file: internal/database/book_first_audio_file.go
// version: 1.0.0
// guid: 5b0f8c3e-7a41-4d92-9e16-c2d7a8b34f05
// last-edited: 2026-10-10

package database

import "github.com/falkcorp/audiobook-organizer/internal/audioext"

// FileLister is the one read FirstAudioFile needs. database.Store satisfies it.
type FileLister interface {
	GetBookFiles(bookID string) ([]BookFile, error)
}

// BookFilesBatcher is the one read FirstAudioFiles needs. database.Store
// satisfies it, so no capability lookup is required.
type BookFilesBatcher interface {
	GetBookFilesForIDsCore(bookIDs []string) (map[string][]BookFileCore, error)
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
// (disc, track, path). ok=false means "no usable file row": the book has no
// rows, or every row is Missing, has an empty path or is not audio per exts.
// It is never an error and NEVER a fallback to Book.FilePath, which is stale
// for many books; the book_file rows are the truth. Callers pass
// audioext.Resolve(configured) for exts.
func FirstAudioFile(store FileLister, bookID string, exts audioext.Set) (f BookFile, ok bool, err error) {
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		return BookFile{}, false, err
	}
	i := firstAudioIndex(files, exts, func(f BookFile) (int, int, string, bool) {
		return f.DiscNumber, f.TrackNumber, f.FilePath, f.Missing
	})
	if i < 0 {
		return BookFile{}, false, nil
	}
	return files[i], true, nil
}

// FirstAudioFiles is the batch form of FirstAudioFile for list pages: one
// store call for N books. The result holds an entry only for books that have a
// usable audio file; absent means ok=false in the single form. It applies the
// same rules and ordering as FirstAudioFile and never reads Book.FilePath.
func FirstAudioFiles(store BookFilesBatcher, bookIDs []string, exts audioext.Set) (map[string]BookFileCore, error) {
	byBook, err := store.GetBookFilesForIDsCore(bookIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]BookFileCore, len(byBook))
	for _, id := range bookIDs {
		rows := byBook[id]
		i := firstAudioIndex(rows, exts, func(f BookFileCore) (int, int, string, bool) {
			return f.DiscNumber, f.TrackNumber, f.FilePath, f.Missing
		})
		if i >= 0 {
			out[id] = rows[i]
		}
	}
	return out, nil
}
