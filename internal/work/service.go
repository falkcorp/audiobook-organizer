// file: internal/work/service.go
// version: 1.1.1
// guid: e9f0g1h2-i3j4-5k6l-7m8n-9o0p1q2r3s4t
// last-edited: 2026-11-09

package work

import (
	"fmt"
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

type WorkService struct {
	db database.WorkStore
}

func NewWorkService(db database.WorkStore) *WorkService {
	return &WorkService{db: db}
}

type WorkListResponse struct {
	Items []database.Work `json:"items"`
	Count int             `json:"count"`
}

func (ws *WorkService) ListWorks() (*WorkListResponse, error) {
	works, err := ws.db.GetAllWorks()
	if err != nil {
		return nil, err
	}
	if works == nil {
		works = []database.Work{}
	}
	return &WorkListResponse{
		Items: works,
		Count: len(works),
	}, nil
}

func (ws *WorkService) CreateWork(work *database.Work) (*database.Work, error) {
	if strings.TrimSpace(work.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	return ws.db.CreateWork(work)
}

func (ws *WorkService) GetWork(id string) (*database.Work, error) {
	work, err := ws.db.GetWorkByID(id)
	if err != nil {
		return nil, err
	}
	if work == nil {
		return nil, fmt.Errorf("work not found")
	}
	return work, nil
}

func (ws *WorkService) UpdateWork(id string, work *database.Work) (*database.Work, error) {
	if strings.TrimSpace(work.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	updated, err := ws.db.UpdateWork(id, work)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (ws *WorkService) DeleteWork(id string) error {
	work, err := ws.db.GetWorkByID(id)
	if err != nil {
		return err
	}
	if work == nil {
		return fmt.Errorf("work not found")
	}
	return ws.db.DeleteWork(id)
}
