// file: internal/metabatch/upgrade_test.go
// version: 1.0.0
// guid: f1a2b3c4-d5e6-7f8a-9b0c-1d2e3f4a5b6c
// last-edited: 2026-06-29

package metabatch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// ptr returns a pointer to the given string — test helper.
func ptr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// transcriptionConfirmsCandidate
// ---------------------------------------------------------------------------

func TestTranscriptionConfirmsCandidate_TitleAndAuthorMatch(t *testing.T) {
	book := &database.Book{
		TranscribedTitle:  ptr("Project Hail Mary"),
		TranscribedAuthor: ptr("Andy Weir"),
	}
	c := &metafetch.MetadataCandidate{
		Title:  "Project Hail Mary",
		Author: "Andy Weir",
		Score:  0.87,
	}
	if !transcriptionConfirmsCandidate(book, c) {
		t.Error("expected transcription to confirm when title and author both match")
	}
}

func TestTranscriptionConfirmsCandidate_TitleMatchNoAuthor(t *testing.T) {
	// No transcribed author — title match alone should confirm.
	book := &database.Book{
		TranscribedTitle: ptr("Dune"),
	}
	c := &metafetch.MetadataCandidate{
		Title:  "Dune",
		Author: "Frank Herbert",
		Score:  0.87,
	}
	if !transcriptionConfirmsCandidate(book, c) {
		t.Error("expected transcription to confirm with title match when no author available")
	}
}

func TestTranscriptionConfirmsCandidate_TitleMatchShortAuthor(t *testing.T) {
	// Transcribed author is ≤3 chars — too short to be useful, ignore it.
	book := &database.Book{
		TranscribedTitle:  ptr("Dune"),
		TranscribedAuthor: ptr("F"),
	}
	c := &metafetch.MetadataCandidate{
		Title:  "Dune",
		Author: "Frank Herbert",
		Score:  0.87,
	}
	if !transcriptionConfirmsCandidate(book, c) {
		t.Error("expected transcription to confirm when transcribed author is too short to be useful")
	}
}

func TestTranscriptionConfirmsCandidate_TitleMismatch(t *testing.T) {
	book := &database.Book{
		TranscribedTitle:  ptr("The Martian"),
		TranscribedAuthor: ptr("Andy Weir"),
	}
	c := &metafetch.MetadataCandidate{
		Title:  "Project Hail Mary",
		Author: "Andy Weir",
		Score:  0.87,
	}
	if transcriptionConfirmsCandidate(book, c) {
		t.Error("expected no confirmation when titles differ")
	}
}

func TestTranscriptionConfirmsCandidate_AuthorMismatch(t *testing.T) {
	book := &database.Book{
		TranscribedTitle:  ptr("Project Hail Mary"),
		TranscribedAuthor: ptr("Brandon Sanderson"),
	}
	c := &metafetch.MetadataCandidate{
		Title:  "Project Hail Mary",
		Author: "Andy Weir",
		Score:  0.87,
	}
	if transcriptionConfirmsCandidate(book, c) {
		t.Error("expected no confirmation when authors differ")
	}
}

func TestTranscriptionConfirmsCandidate_NoTranscription(t *testing.T) {
	// Book has no transcription data at all.
	book := &database.Book{}
	c := &metafetch.MetadataCandidate{
		Title:  "Dune",
		Author: "Frank Herbert",
		Score:  0.87,
	}
	if transcriptionConfirmsCandidate(book, c) {
		t.Error("expected no confirmation when book has no transcription")
	}
}

func TestTranscriptionConfirmsCandidate_AuthorSubstringMatch(t *testing.T) {
	// Transcribed author is a substring of the candidate's author field
	// (e.g., last-name-only transcript vs "First Last" in metadata).
	book := &database.Book{
		TranscribedTitle:  ptr("The Way of Kings"),
		TranscribedAuthor: ptr("Sanderson"),
	}
	c := &metafetch.MetadataCandidate{
		Title:  "The Way of Kings",
		Author: "Brandon Sanderson",
		Score:  0.87,
	}
	if !transcriptionConfirmsCandidate(book, c) {
		t.Error("expected confirmation when transcribed author is a substring of candidate author")
	}
}

// ---------------------------------------------------------------------------
// Gate threshold scenarios (TASK-03 requirements)
// ---------------------------------------------------------------------------

// TestUpgradeGate_TranscriptionMatchBelowGlobalThreshold verifies that a
// score of 0.87 passes when transcription confirms the candidate (gate=0.85)
// but would be rejected without transcription (gate=0.90).
func TestUpgradeGate_TranscriptionMatchBelowGlobalThreshold(t *testing.T) {
	score := 0.87
	// With transcription: gate is MinUpgradeConfidenceWithTranscription=0.85.
	if score < MinUpgradeConfidenceWithTranscription {
		t.Errorf("score %.2f should pass the transcription gate %.2f", score, MinUpgradeConfidenceWithTranscription)
	}
	// Without transcription: gate is MinUpgradeConfidence=0.90 — should fail.
	if score >= MinUpgradeConfidence {
		t.Errorf("score %.2f should NOT pass the global gate %.2f when transcription does not confirm", score, MinUpgradeConfidence)
	}
}

// TestUpgradeGate_NoTranscriptionBelowThreshold verifies that score=0.87
// without a transcription match is rejected by the global gate.
func TestUpgradeGate_NoTranscriptionBelowThreshold(t *testing.T) {
	score := 0.87
	gate := MinUpgradeConfidence // transcriptionConfirms=false
	if score >= gate {
		t.Errorf("score %.2f should be rejected by gate %.2f when transcription does not confirm", score, gate)
	}
}

// TestUpgradeGate_HighScoreAlwaysPasses verifies that score=0.95 passes
// both gates regardless of transcription.
func TestUpgradeGate_HighScoreAlwaysPasses(t *testing.T) {
	score := 0.95
	if score < MinUpgradeConfidence {
		t.Errorf("score %.2f should pass the global gate %.2f", score, MinUpgradeConfidence)
	}
	if score < MinUpgradeConfidenceWithTranscription {
		t.Errorf("score %.2f should also pass the transcription gate %.2f", score, MinUpgradeConfidenceWithTranscription)
	}
}
