// file: internal/server/author_series_service_test.go
// version: 1.0.1
// guid: a7b8c9d0-e1f2-3a4b-5c6d-7e8f9a0b1c2d

package server

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func TestAuthorSeriesService_ListAuthors_Empty(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			return nil, nil
		},
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(mockDB)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	as := NewAuthorSeriesService(mockDB)

	resp, err := as.ListAuthors()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("expected count 0, got %d", resp.Count)
	}
}

func TestAuthorSeriesService_ListSeries_Empty(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllSeriesFunc: func() ([]database.Series, error) {
			return nil, nil
		},
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(mockDB)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	as := NewAuthorSeriesService(mockDB)

	resp, err := as.ListSeries()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("expected count 0, got %d", resp.Count)
	}
}
