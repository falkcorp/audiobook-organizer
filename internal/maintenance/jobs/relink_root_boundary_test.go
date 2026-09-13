// file: internal/maintenance/jobs/relink_root_boundary_test.go
// version: 1.0.0
// guid: 2c7d9e14-8b5a-4f36-9e01-a3f6b8d2c570
// last-edited: 2026-09-12

package jobs

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type recordingBookFileMutator struct {
	files   []database.BookFile
	updated map[string]string
}

func (m *recordingBookFileMutator) GetBookFiles(string) ([]database.BookFile, error) {
	return m.files, nil
}

func (m *recordingBookFileMutator) UpdateBookFile(id string, f *database.BookFile) error {
	if m.updated == nil {
		m.updated = map[string]string{}
	}
	m.updated[id] = f.FilePath
	return nil
}

// rmt_updateBookFiles rewrites only rows under the organizer root. A row in a
// sibling directory ("/lib2" next to root "/lib") is not the organizer's copy
// and must keep its path.
func TestRmtUpdateBookFiles_SiblingOfRootUntouched(t *testing.T) {
	m := &recordingBookFileMutator{files: []database.BookFile{
		{ID: "in", FilePath: "/lib/Author/Title/a.m4b"},
		{ID: "root", FilePath: "/lib"},
		{ID: "sibling", FilePath: "/lib2/Author/Title/a.m4b"},
	}}
	rmt_updateBookFiles(m, "b1", "/itunes/Author/a.m4b", nil, "/lib")

	for _, id := range []string{"in", "root"} {
		if m.updated[id] != "/itunes/Author/a.m4b" {
			t.Errorf("row %q under root not rewritten: %q", id, m.updated[id])
		}
	}
	if p, ok := m.updated["sibling"]; ok {
		t.Errorf("sibling row rewritten to %q", p)
	}
}
