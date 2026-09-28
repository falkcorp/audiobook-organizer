// file: internal/scanner/oversized_dir.go
// version: 1.0.0
// guid: f7c8efaa-7942-41b9-b42d-5a5bb1e2d51f
// last-edited: 2026-09-28

package scanner

import (
	"context"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// subGroupOversizedAlbum handles an album group holding more than
// maxDirectoryBookFiles files in one flat directory: too many to be one book
// (the refusal stands -- see maxDirectoryBookFiles), and far too structured to
// be one book per file.
//
// Until 2026-09-28 the fallback WAS one book per file. A flat Bible folder of
// 1,188 chapter files became 1,188 books, "Will of the Empress" 862,
// "Foundation" 668, and each fragment was then organized away from the folder
// its real book lived in. The files are instead run through the same
// chapter-sequence grouping the untagged files get (consolidateChapterGroups):
//
//   - a flat single work ("01 Genesis 001" … "66 Revelation 022", "Title 001" …)
//     groups by its filename stem into one book per stem, each ≤ the ceiling;
//   - an author shelf ("Mistborn 1", "Mistborn 2", "Elantris" -- whole books,
//     each long) is NOT merged: consolidation requires every file in a group to
//     be short, so each file still stands alone, which is what a shelf is;
//   - a single stem still over the ceiling is refused outright with a warning
//     and no book, rather than shattered or truncated.
//
// Every multi-file book returned is marked sharesDirectory: several books now
// come out of one folder, and normalizing each one's FilePath to that folder
// would give them all the same path.
func subGroupOversizedAlbum(ctx context.Context, files []string) []Book {
	logging.Warn(ctx, "scanner refusing album group as one book: too many files; sub-grouping by filename stem",
		"dir", filepath.Dir(files[0]),
		"count", len(files),
		"limit", maxDirectoryBookFiles,
	)
	books := consolidateChapterGroups(ctx, files)
	for i := range books {
		if len(books[i].SegmentFiles) > 1 {
			books[i].sharesDirectory = true
		}
	}
	return books
}
