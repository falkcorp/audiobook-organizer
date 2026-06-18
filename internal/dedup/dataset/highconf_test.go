// file: internal/dedup/dataset/highconf_test.go
// version: 1.0.0
// guid: 6d2a8f31-0c54-4b29-8e17-9a3b5c7d2e60
// last-edited: 2026-06-18

package dataset

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func strp(s string) *string { return &s }

func TestMineHighConfidenceDup_SharedFileHash(t *testing.T) {
	a := &database.Book{Title: "A"}
	b := &database.Book{Title: "B"}
	aFiles := []database.BookFile{{FilePath: "/a.m4b", FileHash: "deadbeefcafe0001", FileSize: 5 << 20}}
	bFiles := []database.BookFile{{FilePath: "/b.m4b", FileHash: "deadbeefcafe0001", FileSize: 5 << 20}}

	label, reason, fires := MineHighConfidenceDup(a, b, aFiles, bFiles)
	if !fires || label != "true_dup" {
		t.Fatalf("expected true_dup fire; got label=%q fires=%v", label, fires)
	}
	if want := "shared file hash deadbeefcafe"; reason != want {
		t.Errorf("reason=%q want %q", reason, want)
	}
}

func TestMineHighConfidenceDup_SharedRecordingID(t *testing.T) {
	a := &database.Book{Title: "A"}
	b := &database.Book{Title: "B"}
	aFiles := []database.BookFile{{FilePath: "/a.m4b", AcoustIDOnlineRecordingID: "rec-123", FileSize: 5 << 20}}
	bFiles := []database.BookFile{{FilePath: "/b.m4b", AcoustIDOnlineRecordingID: "rec-123", FileSize: 5 << 20}}

	label, reason, fires := MineHighConfidenceDup(a, b, aFiles, bFiles)
	if !fires || label != "true_dup" {
		t.Fatalf("expected true_dup; got label=%q fires=%v reason=%q", label, fires, reason)
	}
	if reason != "shared acoustid recording rec-123" {
		t.Errorf("reason=%q", reason)
	}
}

func TestMineHighConfidenceDup_SharedASIN_WithAudio(t *testing.T) {
	a := &database.Book{Title: "A", ASIN: strp("B00ABC")}
	b := &database.Book{Title: "B", ASIN: strp("B00ABC")}
	// Both sides have plausible audio (positive duration).
	aFiles := []database.BookFile{{FilePath: "/a.m4b", Duration: 3600}}
	bFiles := []database.BookFile{{FilePath: "/b.m4b", Duration: 3600}}

	label, reason, fires := MineHighConfidenceDup(a, b, aFiles, bFiles)
	if !fires || label != "true_dup" || reason != "shared ASIN B00ABC" {
		t.Fatalf("expected shared ASIN true_dup; got label=%q reason=%q fires=%v", label, reason, fires)
	}
}

func TestMineHighConfidenceDup_SharedASIN_NoAudio_DoesNotFire(t *testing.T) {
	a := &database.Book{Title: "A", ASIN: strp("B00ABC")}
	b := &database.Book{Title: "B", ASIN: strp("B00ABC")}
	// Stub sides: zero duration AND sub-floor file size → not plausible audio.
	aFiles := []database.BookFile{{FilePath: "/a.m4b", FileSize: 100}}
	bFiles := []database.BookFile{{FilePath: "/b.m4b", FileSize: 100}}

	_, _, fires := MineHighConfidenceDup(a, b, aFiles, bFiles)
	if fires {
		t.Fatal("shared ASIN on two stubs must NOT fire (audio gate)")
	}
}

func TestMineHighConfidenceDup_NoSignal(t *testing.T) {
	a := &database.Book{Title: "A", ASIN: strp("B001")}
	b := &database.Book{Title: "B", ASIN: strp("B002")}
	aFiles := []database.BookFile{{FilePath: "/a.m4b", FileHash: "h1", Duration: 3600}}
	bFiles := []database.BookFile{{FilePath: "/b.m4b", FileHash: "h2", Duration: 3600}}

	if _, _, fires := MineHighConfidenceDup(a, b, aFiles, bFiles); fires {
		t.Fatal("distinct hashes/ids must not fire")
	}
}

func TestMineHighConfidenceDup_NilBooks(t *testing.T) {
	if _, _, fires := MineHighConfidenceDup(nil, nil, nil, nil); fires {
		t.Fatal("nil books must not fire")
	}
}

// Priority: file-hash beats ASIN when both present.
func TestMineHighConfidenceDup_HashBeatsASIN(t *testing.T) {
	a := &database.Book{ASIN: strp("B00X")}
	b := &database.Book{ASIN: strp("B00X")}
	aFiles := []database.BookFile{{FileHash: "samehash00000", Duration: 60}}
	bFiles := []database.BookFile{{FileHash: "samehash00000", Duration: 60}}
	_, reason, fires := MineHighConfidenceDup(a, b, aFiles, bFiles)
	if !fires || reason[:6] != "shared" || reason != "shared file hash samehash0000" {
		t.Fatalf("expected file-hash reason to win; got %q fires=%v", reason, fires)
	}
}
