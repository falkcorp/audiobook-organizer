// file: internal/plugins/maintenance/intro_transcribe_status_test.go
// version: 1.0.0
// guid: 5a1c9e73-2b48-4f60-8d31-6c0af5b2e719
// last-edited: 2026-07-01

package maintenance

import (
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// newOutcomeStore returns a MockStore that captures every UpdateBook payload.
func newOutcomeStore(written *[]database.Book) *database.MockStore {
	return &database.MockStore{
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			*written = append(*written, *b)
			return b, nil
		},
	}
}

// TestApplyOutcome_UnparsedStoresTranscriptButNoTitle verifies that when Whisper
// returns text the parser can't title, applyOutcome stores the raw transcript,
// leaves TranscribedTitle nil (so OnlyParsedTranscription excludes it), records
// the Unparsed bucket, and still counts the book as processed.
func TestApplyOutcome_UnparsedStoresTranscriptButNoTitle(t *testing.T) {
	var written []database.Book
	store := newOutcomeStore(&written)
	p := New(fakeDeps{store: store})
	accum := newTranscribeStatsAccum(nil, "op", 0, time.Now())
	now := time.Now()

	book := database.Book{ID: "b1"}
	got := p.applyOutcome(store, slog.Default(), &book, statusUnparsed,
		"", "mumbled words with no title structure", transcribe.IntroFields{}, now, accum)

	if !got {
		t.Fatal("unparsed transcript should count as processed (return true)")
	}
	if len(written) != 1 {
		t.Fatalf("expected exactly 1 UpdateBook, got %d", len(written))
	}
	w := written[0]
	if w.TranscribeStatus == nil || *w.TranscribeStatus != statusUnparsed {
		t.Errorf("TranscribeStatus = %v, want %q", w.TranscribeStatus, statusUnparsed)
	}
	if w.IntroTranscription == nil || *w.IntroTranscription == "" {
		t.Error("raw transcript must be stored for unparsed (needed by reparse_only)")
	}
	if w.TranscribedTitle != nil {
		t.Errorf("TranscribedTitle must stay nil for unparsed, got %q", *w.TranscribedTitle)
	}
	if snap := accum.snapshot(); snap.Unparsed != 1 || snap.OK != 0 {
		t.Errorf("accum: Unparsed=%d OK=%d, want Unparsed=1 OK=0", snap.Unparsed, snap.OK)
	}
}

// TestApplyOutcome_OKStoresParsedTitle verifies the statusOK path stores the
// parsed fields and increments OK (not Unparsed).
func TestApplyOutcome_OKStoresParsedTitle(t *testing.T) {
	var written []database.Book
	store := newOutcomeStore(&written)
	p := New(fakeDeps{store: store})
	accum := newTranscribeStatsAccum(nil, "op", 0, time.Now())
	now := time.Now()

	book := database.Book{ID: "b2"}
	fields := transcribe.IntroFields{Title: "Salem's Lot", Author: "Stephen King", Narrator: "Ron McClarty"}
	got := p.applyOutcome(store, slog.Default(), &book, statusOK,
		"", "Salem's Lot by Stephen King read by Ron McClarty", fields, now, accum)

	if !got {
		t.Fatal("ok transcript should count as processed (return true)")
	}
	w := written[0]
	if w.TranscribedTitle == nil || *w.TranscribedTitle != "Salem's Lot" {
		t.Errorf("TranscribedTitle = %v, want \"Salem's Lot\"", w.TranscribedTitle)
	}
	if w.TranscribedNarrator == nil || *w.TranscribedNarrator != "Ron McClarty" {
		t.Errorf("TranscribedNarrator = %v, want \"Ron McClarty\"", w.TranscribedNarrator)
	}
	if snap := accum.snapshot(); snap.OK != 1 || snap.Unparsed != 0 {
		t.Errorf("accum: OK=%d Unparsed=%d, want OK=1 Unparsed=0", snap.OK, snap.Unparsed)
	}
}
