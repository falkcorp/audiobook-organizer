// file: internal/reconcile/itunes_heal_db_resolve_test.go
// version: 1.1.0
// guid: b04ad264-d893-44f7-bd8c-d7f85f1b2385
// last-edited: 2026-09-19

package reconcile

import (
	"context"
	"encoding/binary"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func healTestFrames(n int, seed uint32) []byte {
	b := make([]byte, n*4)
	for i := range n {
		binary.LittleEndian.PutUint32(b[i*4:], seed*2654435761+uint32(i)*40503)
	}
	return b
}

// TestResolveAmbiguousByDB_UnreadablePrintIsUnknownNotMerge pins the
// fail-safe for a stored fingerprint that cannot be compared: the resolver
// must neither merge the candidates nor claim a winner — it returns
// ("", 0) so the heal falls through to its next layer, exactly as for
// candidates that are simply not the same audio. The control case proves the
// fixture reaches the merge path when the prints do agree.
func TestResolveAmbiguousByDB_UnreadablePrintIsUnknownNotMerge(t *testing.T) {
	good := healTestFrames(400, 1)
	cases := map[string]struct {
		second    []byte
		wantMerge bool
	}{
		"misaligned_print": {second: append(append([]byte{}, good...), 0xff), wantMerge: false},
		"control_same":     {second: good, wantMerge: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var mergeReads atomic.Int32
			m := &database.MockStore{}
			m.GetBookFileByPathFunc = func(p string) (*database.BookFile, error) {
				fp := good
				bookID := "A"
				if p == "/b.m4b" {
					fp, bookID = tc.second, "B"
				}
				return &database.BookFile{ID: "f" + bookID, BookID: bookID, FilePath: p, AcoustIDFingerprint: fp, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}, nil
			}
			m.GetBookByIDFunc = func(id string) (*database.Book, error) {
				mergeReads.Add(1)
				return nil, nil
			}
			src, merged := resolveAmbiguousByDB(context.Background(), m, []string{"/a.m4b", "/b.m4b"})
			if tc.wantMerge {
				if mergeReads.Load() == 0 {
					t.Fatal("control: identical prints never reached the merge path; fixture is vacuous")
				}
				return
			}
			if src != "" || merged != 0 {
				t.Fatalf("unreadable print resolved to %q (merged %d), want unresolved", src, merged)
			}
			if mergeReads.Load() != 0 {
				t.Fatal("unreadable print reached the merge path")
			}
		})
	}
}
