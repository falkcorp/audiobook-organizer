// file: internal/scanner/rescan_preserve_test.go
// version: 1.0.0
// guid: b2f4c6a8-1d3e-4f50-9a7b-2c6e8d0f1a34
// last-edited: 2026-07-13

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func strPtr(s string) *string   { return &s }
func intP(i int) *int           { return &i }
func f64Ptr(f float64) *float64 { return &f }

// TestSaveBookToDatabase_RescanPreservesEnrichedFields is the load-bearing
// regression test for the rescan data-loss bug: re-scanning an already-imported
// file (matched by path) used to write a partial scanner Book literal via a
// full-replace UpdateBook, wiping every field the scanner does not populate —
// Author/Series, ratings, Whisper transcriptions, MetadataReviewStatus, Genre,
// media-info, etc. This test drives the REAL saveBookToDatabase +
// PebbleStore.UpdateBook path (not applyScannerFields in isolation) so it also
// proves the store getter returns a full-fidelity row.
func TestSaveBookToDatabase_RescanPreservesEnrichedFields(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()

	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})

	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })

	rootDir := t.TempDir()
	config.AppConfig.RootDir = rootDir

	filePath := filepath.Join(rootDir, "rich-book.m4b")
	if err := os.WriteFile(filePath, []byte("rich book content"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// 1) Initial import with author + series so the links exist.
	orig := &Book{
		FilePath: filePath,
		Title:    "Original Title",
		Author:   "Rich Author",
		Series:   "Rich Series",
		Position: 3,
		Format:   ".m4b",
		Duration: 100,
		Narrator: "Original Narrator",
	}
	if err := saveBookToDatabase(context.Background(), orig); err != nil {
		t.Fatalf("initial saveBookToDatabase failed: %v", err)
	}

	saved, err := store.GetBookByFilePath(filePath)
	if err != nil || saved == nil {
		t.Fatalf("expected saved book after import, err=%v", err)
	}
	if saved.AuthorID == nil || saved.SeriesID == nil {
		t.Fatalf("expected author/series links after import, got author=%v series=%v", saved.AuthorID, saved.SeriesID)
	}
	origAuthorID := *saved.AuthorID
	origSeriesID := *saved.SeriesID

	// 2) Enrich the row with data no scanner tag carries — the exact set the
	// old rescan wiped. Persisted via a full-replace UpdateBook, which the
	// store does NOT protect for these fields.
	saved.AudibleRatingOverall = f64Ptr(4.7)
	saved.AudibleRatingCount = intP(1234)
	saved.GoogleRatingAverage = f64Ptr(4.2)
	saved.ITunesRating = intP(100)
	saved.UserRatingOverall = f64Ptr(5.0)
	saved.IntroTranscription = strPtr("This is Rich Title by Rich Author. Read by Original Narrator.")
	saved.TranscribedTitle = strPtr("Rich Title")
	saved.TranscribedAuthor = strPtr("Rich Author")
	saved.TranscribeStatus = strPtr("ok")
	saved.MetadataReviewStatus = strPtr("matched")
	saved.MetadataSource = strPtr("audible")
	saved.MetadataSourceHash = strPtr("sha256:deadbeef")
	saved.Genre = strPtr("Fantasy")
	saved.Bitrate = intP(128)
	saved.Codec = strPtr("aac")
	saved.SampleRate = intP(44100)
	saved.Quality = strPtr("high")
	saved.ITunesSyncStatus = strPtr("synced")
	saved.AudibleRuntimeMin = intP(605)
	if _, err := store.UpdateBook(saved.ID, saved); err != nil {
		t.Fatalf("enrich UpdateBook failed: %v", err)
	}

	// 3) Rescan the SAME path with fresh tag data that changes scanner-owned
	// fields (Title, Narrator) AND carries NO author/series tag — the case that
	// previously produced nil AuthorID/SeriesID and wiped the links.
	rescan := &Book{
		FilePath: filePath,
		Title:    "Rescanned Title",
		Author:   "", // no author tag
		Series:   "", // no series tag
		Format:   ".m4b",
		Duration: 100,
		Narrator: "Rescanned Narrator",
	}
	if err := saveBookToDatabase(context.Background(), rescan); err != nil {
		t.Fatalf("rescan saveBookToDatabase failed: %v", err)
	}

	// 4) Reload and assert: scanner-owned fields updated, everything else survived.
	got, err := store.GetBookByFilePath(filePath)
	if err != nil || got == nil {
		t.Fatalf("expected book after rescan, err=%v", err)
	}

	// Scanner-owned fields DID update.
	if got.Title != "Rescanned Title" {
		t.Errorf("Title: want %q, got %q", "Rescanned Title", got.Title)
	}
	if got.Narrator == nil || *got.Narrator != "Rescanned Narrator" {
		t.Errorf("Narrator: want %q, got %v", "Rescanned Narrator", got.Narrator)
	}

	// Author/Series links SURVIVED (the headline fix).
	if got.AuthorID == nil || *got.AuthorID != origAuthorID {
		t.Errorf("AuthorID wiped on rescan: want %d, got %v", origAuthorID, got.AuthorID)
	}
	if got.SeriesID == nil || *got.SeriesID != origSeriesID {
		t.Errorf("SeriesID wiped on rescan: want %d, got %v", origSeriesID, got.SeriesID)
	}

	// Every previously-wiped enriched field SURVIVED.
	assertF64(t, "AudibleRatingOverall", got.AudibleRatingOverall, 4.7)
	assertInt(t, "AudibleRatingCount", got.AudibleRatingCount, 1234)
	assertF64(t, "GoogleRatingAverage", got.GoogleRatingAverage, 4.2)
	assertInt(t, "ITunesRating", got.ITunesRating, 100)
	assertF64(t, "UserRatingOverall", got.UserRatingOverall, 5.0)
	assertStr(t, "IntroTranscription", got.IntroTranscription, "This is Rich Title by Rich Author. Read by Original Narrator.")
	assertStr(t, "TranscribedTitle", got.TranscribedTitle, "Rich Title")
	assertStr(t, "TranscribedAuthor", got.TranscribedAuthor, "Rich Author")
	assertStr(t, "TranscribeStatus", got.TranscribeStatus, "ok")
	assertStr(t, "MetadataReviewStatus", got.MetadataReviewStatus, "matched")
	assertStr(t, "MetadataSource", got.MetadataSource, "audible")
	assertStr(t, "MetadataSourceHash", got.MetadataSourceHash, "sha256:deadbeef")
	assertStr(t, "Genre", got.Genre, "Fantasy")
	assertInt(t, "Bitrate", got.Bitrate, 128)
	assertStr(t, "Codec", got.Codec, "aac")
	assertInt(t, "SampleRate", got.SampleRate, 44100)
	assertStr(t, "Quality", got.Quality, "high")
	assertStr(t, "ITunesSyncStatus", got.ITunesSyncStatus, "synced")
	assertInt(t, "AudibleRuntimeMin", got.AudibleRuntimeMin, 605)
}

func assertStr(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s wiped on rescan: want %q, got nil", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s: want %q, got %q", field, want, *got)
	}
}

func assertInt(t *testing.T, field string, got *int, want int) {
	t.Helper()
	if got == nil {
		t.Errorf("%s wiped on rescan: want %d, got nil", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s: want %d, got %d", field, want, *got)
	}
}

func assertF64(t *testing.T, field string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s wiped on rescan: want %v, got nil", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s: want %v, got %v", field, want, *got)
	}
}
