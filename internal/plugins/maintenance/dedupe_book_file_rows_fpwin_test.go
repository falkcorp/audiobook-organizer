// file: internal/plugins/maintenance/dedupe_book_file_rows_fpwin_test.go
// version: 1.0.0
// guid: 9578f32b-f95b-4ff6-a7bf-4ceead8fea41
// last-edited: 2026-09-19

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A row merge collapses several book_file rows for ONE file into a keeper and
// deletes the rest. Deleting a row cascades its fingerprint windows, so without
// a carry-over every window computed against a donor row is lost the moment the
// duplicates are collapsed. Each row here holds a different slot, so the keeper
// can only end up with all three if the donors' windows were moved onto it.
func TestDedupeBookFileRows_CarriesDonorWindowsToTheKeeper(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 4, 3
	slots := [copies]int{1000, 5000, 9000}

	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	bookIDs := seedDupBooks(t, s, books, copies)
	allRows := map[string][]string{}
	for _, bookID := range bookIDs {
		files, ferr := s.GetBookFiles(bookID)
		if ferr != nil || len(files) != copies {
			t.Fatalf("seed: GetBookFiles(%s) = %d rows, err %v", bookID, len(files), ferr)
		}
		for i := range files {
			allRows[bookID] = append(allRows[bookID], files[i].ID)
			w := &database.FingerprintWindow{
				Ref:      database.FileWindowRef(files[i].ID),
				Kind:     database.WindowKindWindow,
				SlotBP:   slots[i],
				Pipeline: "ffpcm-s16le-11025-mono/v1",
				Raw:      bytes.Repeat([]byte{byte(i + 1)}, 4*100),
			}
			if perr := s.PutFingerprintWindow(w); perr != nil {
				t.Fatalf("PutFingerprintWindow: %v", perr)
			}
		}
	}

	p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: s}, root: t.TempDir()}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})
	if err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{}); err != nil {
		t.Fatalf("runDedupeBookFileRows: %v", err)
	}

	for _, bookID := range bookIDs {
		files, ferr := s.GetBookFiles(bookID)
		if ferr != nil || len(files) != 1 {
			t.Fatalf("book %s: %d rows survived (err %v), want 1", bookID, len(files), ferr)
		}
		keeper := files[0].ID
		got, gerr := s.GetFingerprintWindows(database.FileWindowRef(keeper))
		if gerr != nil {
			t.Fatalf("GetFingerprintWindows(%s): %v", keeper, gerr)
		}
		if len(got) != copies {
			t.Fatalf("book %s: keeper %s holds %d windows, want %d (donor windows lost in the merge)", bookID, keeper, len(got), copies)
		}
		for i, w := range got {
			if w.SlotBP != slots[i] || w.Ref != database.FileWindowRef(keeper) {
				t.Fatalf("book %s: window %d = slot %d ref %s, want slot %d ref %s", bookID, i, w.SlotBP, w.Ref, slots[i], database.FileWindowRef(keeper))
			}
		}
		for _, id := range allRows[bookID] {
			if id == keeper {
				continue
			}
			left, lerr := s.GetFingerprintWindows(database.FileWindowRef(id))
			if lerr != nil || len(left) != 0 {
				t.Fatalf("book %s: deleted donor %s still has %d windows (err %v)", bookID, id, len(left), lerr)
			}
		}
	}
}
