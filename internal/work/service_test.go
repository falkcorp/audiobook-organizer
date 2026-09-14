// file: internal/work/service_test.go
// version: 1.0.1
// guid: f0g1h2i3-j4k5-6l7m-8n9o-0p1q2r3s4t5u
// last-edited: 2026-09-13

package work

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// MockWorkStore is a mock implementation of database.WorkStore for testing
type MockWorkStore struct {
	works              []database.Work
	getWorkByIDFn      func(id string) (*database.Work, error)
	getAllWorksFn      func() ([]database.Work, error)
	createWorkFn       func(work *database.Work) (*database.Work, error)
	updateWorkFn       func(id string, work *database.Work) (*database.Work, error)
	deleteWorkFn       func(id string) error
	getBooksByWorkIDFn func(workID string) ([]database.Book, error)
}

func (m *MockWorkStore) GetWorkByID(id string) (*database.Work, error) {
	if m.getWorkByIDFn != nil {
		return m.getWorkByIDFn(id)
	}
	for i, w := range m.works {
		if w.ID == id {
			return &m.works[i], nil
		}
	}
	return nil, nil
}

func (m *MockWorkStore) GetAllWorks() ([]database.Work, error) {
	if m.getAllWorksFn != nil {
		return m.getAllWorksFn()
	}
	return m.works, nil
}

func (m *MockWorkStore) CreateWork(work *database.Work) (*database.Work, error) {
	if m.createWorkFn != nil {
		return m.createWorkFn(work)
	}
	work.ID = "generated-id"
	m.works = append(m.works, *work)
	return work, nil
}

func (m *MockWorkStore) UpdateWork(id string, work *database.Work) (*database.Work, error) {
	if m.updateWorkFn != nil {
		return m.updateWorkFn(id, work)
	}
	for i, w := range m.works {
		if w.ID == id {
			m.works[i] = *work
			return &m.works[i], nil
		}
	}
	return nil, errors.New("work not found")
}

func (m *MockWorkStore) DeleteWork(id string) error {
	if m.deleteWorkFn != nil {
		return m.deleteWorkFn(id)
	}
	for i, w := range m.works {
		if w.ID == id {
			m.works = append(m.works[:i], m.works[i+1:]...)
			return nil
		}
	}
	return errors.New("work not found")
}

func (m *MockWorkStore) GetBooksByWorkID(workID string) ([]database.Book, error) {
	if m.getBooksByWorkIDFn != nil {
		return m.getBooksByWorkIDFn(workID)
	}
	return []database.Book{}, nil
}
func (m *MockWorkStore) GetAllWorkBookCounts() (map[string]int, error) { return nil, nil }

func (m *MockWorkStore) ForEachWork(ctx context.Context, visit func(database.Work) error) error {
	works, err := m.GetAllWorks()
	if err != nil {
		return err
	}
	for _, w := range works {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(w); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockWorkStore) WorksGeneration() uint64 { return 0 }

// TestWorkService_ListWorks_Empty tests listing works when there are none
func TestWorkService_ListWorks_Empty(t *testing.T) {
	mockDB := &MockWorkStore{works: []database.Work{}}
	ws := NewWorkService(mockDB)

	resp, err := ws.ListWorks()

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(resp.Items))
	}
	if resp.Count != 0 {
		t.Errorf("expected count 0, got %d", resp.Count)
	}
}

// TestWorkService_ListWorks_WithItems tests listing works with multiple items
func TestWorkService_ListWorks_WithItems(t *testing.T) {
	works := []database.Work{
		{ID: "1", Title: "Work 1"},
		{ID: "2", Title: "Work 2"},
		{ID: "3", Title: "Work 3"},
	}
	mockDB := &MockWorkStore{works: works}
	ws := NewWorkService(mockDB)

	resp, err := ws.ListWorks()

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Items) != 3 {
		t.Errorf("expected 3 items, got %d", len(resp.Items))
	}
	if resp.Count != 3 {
		t.Errorf("expected count 3, got %d", resp.Count)
	}
}

// TestWorkService_ListWorks_DBError tests handling database errors
func TestWorkService_ListWorks_DBError(t *testing.T) {
	mockDB := &MockWorkStore{
		getAllWorksFn: func() ([]database.Work, error) {
			return nil, errors.New("database error")
		},
	}
	ws := NewWorkService(mockDB)

	resp, err := ws.ListWorks()

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}
}

// TestWorkService_CreateWork_Success tests creating a work with valid title
func TestWorkService_CreateWork_Success(t *testing.T) {
	mockDB := &MockWorkStore{works: []database.Work{}}
	ws := NewWorkService(mockDB)

	work := &database.Work{Title: "New Work"}
	created, err := ws.CreateWork(work)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if created == nil {
		t.Fatal("expected non-nil created work")
	}
	if created.Title != "New Work" {
		t.Errorf("expected title 'New Work', got '%s'", created.Title)
	}
}

// TestWorkService_CreateWork_MissingTitle tests validation when title is empty
func TestWorkService_CreateWork_MissingTitle(t *testing.T) {
	mockDB := &MockWorkStore{works: []database.Work{}}
	ws := NewWorkService(mockDB)

	work := &database.Work{Title: ""}
	created, err := ws.CreateWork(work)

	if err == nil {
		t.Fatal("expected error for empty title")
	}
	if created != nil {
		t.Errorf("expected nil created work, got %v", created)
	}
	if err.Error() != "title is required" {
		t.Errorf("expected 'title is required' error, got '%s'", err.Error())
	}
}

// TestWorkService_CreateWork_WhitespaceTitle tests validation with whitespace-only title
func TestWorkService_CreateWork_WhitespaceTitle(t *testing.T) {
	mockDB := &MockWorkStore{works: []database.Work{}}
	ws := NewWorkService(mockDB)

	work := &database.Work{Title: "   \t\n  "}
	created, err := ws.CreateWork(work)

	if err == nil {
		t.Fatal("expected error for whitespace-only title")
	}
	if created != nil {
		t.Errorf("expected nil created work, got %v", created)
	}
}

// TestWorkService_GetWork_Success tests retrieving an existing work
func TestWorkService_GetWork_Success(t *testing.T) {
	works := []database.Work{
		{ID: "1", Title: "Work 1"},
	}
	mockDB := &MockWorkStore{works: works}
	ws := NewWorkService(mockDB)

	work, err := ws.GetWork("1")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if work == nil {
		t.Fatal("expected non-nil work")
	}
	if work.Title != "Work 1" {
		t.Errorf("expected title 'Work 1', got '%s'", work.Title)
	}
}

// TestWorkService_GetWork_NotFound tests retrieving a non-existent work
func TestWorkService_GetWork_NotFound(t *testing.T) {
	mockDB := &MockWorkStore{works: []database.Work{}}
	ws := NewWorkService(mockDB)

	work, err := ws.GetWork("nonexistent")

	if err == nil {
		t.Fatal("expected error for non-existent work")
	}
	if work != nil {
		t.Errorf("expected nil work, got %v", work)
	}
	if err.Error() != "work not found" {
		t.Errorf("expected 'work not found' error, got '%s'", err.Error())
	}
}

// TestWorkService_GetWork_DBError tests handling database errors
func TestWorkService_GetWork_DBError(t *testing.T) {
	mockDB := &MockWorkStore{
		getWorkByIDFn: func(id string) (*database.Work, error) {
			return nil, errors.New("database error")
		},
	}
	ws := NewWorkService(mockDB)

	work, err := ws.GetWork("1")

	if err == nil {
		t.Fatal("expected error from database")
	}
	if work != nil {
		t.Errorf("expected nil work, got %v", work)
	}
}

// TestWorkService_UpdateWork_Success tests updating an existing work
func TestWorkService_UpdateWork_Success(t *testing.T) {
	works := []database.Work{
		{ID: "1", Title: "Old Title"},
	}
	mockDB := &MockWorkStore{works: works}
	ws := NewWorkService(mockDB)

	updated, err := ws.UpdateWork("1", &database.Work{ID: "1", Title: "New Title"})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if updated == nil {
		t.Fatal("expected non-nil updated work")
	}
	if updated.Title != "New Title" {
		t.Errorf("expected title 'New Title', got '%s'", updated.Title)
	}
}

// TestWorkService_UpdateWork_MissingTitle tests validation during update
func TestWorkService_UpdateWork_MissingTitle(t *testing.T) {
	works := []database.Work{
		{ID: "1", Title: "Old Title"},
	}
	mockDB := &MockWorkStore{works: works}
	ws := NewWorkService(mockDB)

	updated, err := ws.UpdateWork("1", &database.Work{ID: "1", Title: ""})

	if err == nil {
		t.Fatal("expected error for empty title")
	}
	if updated != nil {
		t.Errorf("expected nil updated work, got %v", updated)
	}
}

// TestWorkService_DeleteWork_Success tests deleting an existing work
func TestWorkService_DeleteWork_Success(t *testing.T) {
	works := []database.Work{
		{ID: "1", Title: "Work 1"},
	}
	mockDB := &MockWorkStore{works: works}
	ws := NewWorkService(mockDB)

	err := ws.DeleteWork("1")

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	// Verify it's actually deleted
	remaining, _ := mockDB.GetAllWorks()
	if len(remaining) != 0 {
		t.Errorf("expected 0 works after deletion, got %d", len(remaining))
	}
}

// TestWorkService_DeleteWork_NotFound tests deleting a non-existent work
func TestWorkService_DeleteWork_NotFound(t *testing.T) {
	mockDB := &MockWorkStore{works: []database.Work{}}
	ws := NewWorkService(mockDB)

	err := ws.DeleteWork("nonexistent")

	if err == nil {
		t.Fatal("expected error for non-existent work")
	}
}
