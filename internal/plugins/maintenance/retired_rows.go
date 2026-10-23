// file: internal/plugins/maintenance/retired_rows.go
// version: 1.0.0
// guid: 8b2d4e61-9c07-4f35-a1d8-3e5f60b7c249
// last-edited: 2026-10-23

package maintenance

import "github.com/falkcorp/audiobook-organizer/internal/database"

// countLiveCores is the number of rows in cores that are not retired book
// versions. The plans that read the whole table INCLUDING retired rows (so a
// retired row's path counts as a claim) report their scanned-row figure over
// the rows they can actually repair, which are the live ones.
func countLiveCores(cores []database.BookFileCore) int {
	n := 0
	for i := range cores {
		if !cores[i].Retired {
			n++
		}
	}
	return n
}
