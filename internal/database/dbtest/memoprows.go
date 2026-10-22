// file: internal/database/dbtest/memoprows.go
// version: 1.0.0
// guid: d2169091-2ad9-44a3-aa72-502009e2a522
// last-edited: 2026-10-22

package dbtest

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// MemOpRows is an in-memory database.OpRowStore for tests whose store is a
// hand-written fake: embed it to give the fake the plan-row capability without
// opening Pebble. TestOpRowStoresAgree holds it to the same behaviour as
// PebbleStore's implementation.
type MemOpRows struct {
	mu   sync.Mutex
	rows map[string]map[string]database.OpRow // op id -> row id -> row
}

var _ database.OpRowStore = (*MemOpRows)(nil)

// NewMemOpRows returns an empty MemOpRows.
func NewMemOpRows() *MemOpRows { return &MemOpRows{rows: map[string]map[string]database.OpRow{}} }

func clone(b []byte) []byte { return append([]byte(nil), b...) }

// ReplaceOpRows implements database.OpRowStore.
func (m *MemOpRows) ReplaceOpRows(opID string, n int, at func(i int) (database.OpRow, error)) error {
	if opID == "" {
		return errors.New("opv2: ReplaceOpRows: empty operation id")
	}
	set := make(map[string]database.OpRow, n)
	for i := 0; i < n; i++ {
		row, err := at(i)
		if err != nil {
			return err
		}
		if row.ID == "" {
			return fmt.Errorf("opv2: ReplaceOpRows %s: row %d has no id", opID, i)
		}
		set[row.ID] = database.OpRow{ID: row.ID, Index: clone(row.Index), Data: clone(row.Data)}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]map[string]database.OpRow{}
	}
	if n == 0 {
		delete(m.rows, opID)
		return nil
	}
	m.rows[opID] = set
	return nil
}

func (m *MemOpRows) sortedIDs(opID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.rows[opID]))
	for id := range m.rows[opID] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ScanOpRowIndex implements database.OpRowStore.
func (m *MemOpRows) ScanOpRowIndex(opID string, fn func(rowID string, index []byte) error) error {
	for _, id := range m.sortedIDs(opID) {
		m.mu.Lock()
		row := m.rows[opID][id]
		m.mu.Unlock()
		if err := fn(id, row.Index); err != nil {
			return err
		}
	}
	return nil
}

// GetOpRows implements database.OpRowStore.
func (m *MemOpRows) GetOpRows(opID string, rowIDs []string) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(rowIDs))
	for i, id := range rowIDs {
		if row, ok := m.rows[opID][id]; ok {
			out[i] = clone(row.Data)
		}
	}
	return out, nil
}
