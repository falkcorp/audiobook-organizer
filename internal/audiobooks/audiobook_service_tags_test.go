// file: internal/audiobooks/audiobook_service_tags_test.go
// version: 1.0.1
// guid: 6f6a8d8d-0c5d-4c0a-9f1d-5b9ad01a5b42

package audiobooks

import (
	"context"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

type stubMetadataExtractor struct {
	meta metadata.Metadata
	err  error
}

func (s stubMetadataExtractor) ExtractMetadata(_ string) (metadata.Metadata, error) {
	return s.meta, s.err
}

func TestGetAudiobookTags_UsesSnapshotComparisonValues(t *testing.T) {
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{
				ID:    id,
				Title: "Current Title",
			}, nil
		},
		GetBookAtVersionFunc: func(id string, ts time.Time) (*database.Book, error) {
			if id != "book-1" {
				t.Fatalf("unexpected snapshot book id: %s", id)
			}
			if !ts.Equal(timestamp) {
				t.Fatalf("unexpected snapshot timestamp: %s", ts)
			}
			publisher := "Old Publisher"
			return &database.Book{
				ID:        id,
				Title:     "Snapshot Title",
				Publisher: &publisher,
			}, nil
		},
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return nil, nil
		},
	}

	oldGlobal := database.GetGlobalStore()
	database.SetGlobalStore(store)
	defer func() { database.SetGlobalStore(oldGlobal) }()

	svc := NewAudiobookService(store)
	resp, err := svc.GetAudiobookTags(context.Background(), "book-1", "", timestamp.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("GetAudiobookTags returned error: %v", err)
	}

	tags, ok := resp["tags"].(map[string]database.MetadataProvenanceEntry)
	if !ok {
		t.Fatalf("unexpected tags type: %T", resp["tags"])
	}

	if got := tags["title"].ComparisonValue; got != "Snapshot Title" {
		t.Fatalf("expected snapshot title comparison value, got %#v", got)
	}
	if got := tags["publisher"].ComparisonValue; got != "Old Publisher" {
		t.Fatalf("expected snapshot publisher comparison value, got %#v", got)
	}
}

func TestExtractBookFileMetadata_FallsBackToDatabaseAuthorWhenArtistMatchesNarrator(t *testing.T) {
	metadata.SetMetadataExtractor(stubMetadataExtractor{
		meta: metadata.Metadata{
			Artist:              "Narrator Name",
			Narrator:            "Narrator Name",
			OrganizerTagVersion: "",
		},
	})
	defer func() { metadata.SetMetadataExtractor(nil) }()

	svc := NewAudiobookService(&database.MockStore{})
	meta := svc.extractBookFileMetadata(&database.Book{FilePath: "/tmp/book.m4b"}, "Database Author")

	if meta.Artist != "Database Author" {
		t.Fatalf("expected database author fallback, got %q", meta.Artist)
	}
	if meta.AuthorSource != "database author fallback" {
		t.Fatalf("expected author source to describe database fallback, got %q", meta.AuthorSource)
	}
}
