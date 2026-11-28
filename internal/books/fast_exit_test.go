// file: internal/books/fast_exit_test.go
// version: 0.1.0
// guid: 6b1e8f42-3c97-4a05-9d2e-8f7a1c4b0e63
// last-edited: 2026-11-20

package books

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/app"
)

// TestFastExitModule_PrintsTheFlipLevel (F3-07 rehearsal finding 3): the
// level vector --print-storage-levels prints is the one a serve boot raises
// the store to, books at the cutover level, not the bare Module()'s 0.
func TestFastExitModule_PrintsTheFlipLevel(t *testing.T) {
	var out bytes.Buffer
	handled, err := app.FastExit([]string{"audiobook-organizer", "--print-storage-levels"}, &out, FastExitModule())
	if !handled || err != nil {
		t.Fatalf("FastExit = %v, %v", handled, err)
	}
	var levels map[string]int
	if err := json.Unmarshal(out.Bytes(), &levels); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	// The identity flip is on (B10g), so books' level is the identity level.
	if levels[string(ID)] != cutover.BooksIdentityLevel {
		t.Errorf("printed %s level %d, want the identity level %d (output %s)", ID, levels[string(ID)], cutover.BooksIdentityLevel, out.String())
	}
}
