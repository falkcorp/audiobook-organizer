// file: internal/testutil/rapidgen/rapidgen_test.go
// version: 1.0.1
// guid: 2931b48a-8535-4f2a-a455-cf322f99cea5

package rapidgen

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
	"pgregory.net/rapid"
)

// Each TestGen_* verifies the generator produces values that satisfy the
// invariant the plan calls out for it: non-empty titles, valid statuses, etc.
// The generators will be used by every downstream property test, so we smoke
// them here to fail fast if a shrink ever produces an invalid value.

func TestGen_Book(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		b := Book(t)
		if b == nil {
			t.Fatal("Book returned nil")
			return // Unreachable, but satisfies static analyzer
		}
		if b.Title == "" {
			t.Errorf("Title must be non-empty, got %q", b.Title)
		}
		if b.FilePath == "" {
			t.Errorf("FilePath must be non-empty")
		}
		if b.Format == "" {
			t.Errorf("Format must be non-empty")
		}
		if b.ID != "" {
			t.Errorf("ID must be empty (CreateBook assigns ULID), got %q", b.ID)
		}
	})
}

func TestGen_Author(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := Author(t)
		if a.Name == "" {
			t.Errorf("Author.Name must be non-empty")
		}
		if a.ID != 0 {
			t.Errorf("Author.ID must be 0 (CreateAuthor assigns), got %d", a.ID)
		}
	})
}

func TestGen_Series(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := Series(t)
		if s.Name == "" {
			t.Errorf("Series.Name must be non-empty")
		}
		if s.ID != 0 {
			t.Errorf("Series.ID must be 0, got %d", s.ID)
		}
	})
}

func TestGen_BookFile(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		bookID := "01H" + strings.Repeat("A", 23) // fake ULID
		f := BookFile(t, bookID)
		if f.BookID != bookID {
			t.Errorf("BookID must propagate, got %q", f.BookID)
		}
		if f.FilePath == "" {
			t.Errorf("FilePath must be non-empty")
		}
		if f.Format == "" {
			t.Errorf("Format must be non-empty")
		}
		if f.Duration == nil {
			t.Errorf("Duration must be positive, got %d", durtest.Sec(f.Duration))
		}
		if f.TrackCount < f.TrackNumber {
			t.Errorf("TrackCount (%d) must be >= TrackNumber (%d)", f.TrackCount, f.TrackNumber)
		}
	})
}

func TestGen_BookVersion(t *testing.T) {
	validStatuses := make(map[string]struct{}, len(bookVersionStatuses))
	for _, s := range bookVersionStatuses {
		validStatuses[s] = struct{}{}
	}
	rapid.Check(t, func(t *rapid.T) {
		bookID := "book-" + rapid.StringMatching(`[a-z0-9]{6}`).Draw(t, "bid")
		v := BookVersion(t, bookID)
		if v.BookID != bookID {
			t.Errorf("BookID must propagate, got %q", v.BookID)
		}
		if _, ok := validStatuses[v.Status]; !ok {
			t.Errorf("Status %q not in valid set", v.Status)
		}
		if v.Format == "" {
			t.Errorf("Format must be non-empty")
		}
		if v.Source == "" {
			t.Errorf("Source must be non-empty")
		}
		if v.ID != "" {
			t.Errorf("ID must be empty (CreateBookVersion assigns), got %q", v.ID)
		}
		if v.IngestDate.IsZero() {
			t.Errorf("IngestDate must be set")
		}
	})
}

func TestGen_BookVersionActive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := BookVersionActive(t, "b1")
		if v.Status != database.BookVersionStatusActive {
			t.Errorf("Status must be active, got %q", v.Status)
		}
	})
}

func TestGen_User(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		u, e, ph := User(t)
		if len(u) < 3 || len(u) > 24 {
			t.Errorf("Username length out of range: %q (len=%d)", u, len(u))
		}
		for _, r := range u {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				t.Errorf("Username %q has non-alnum-lowercase rune %q", u, r)
			}
		}
		if !strings.Contains(e, "@") || strings.Count(e, "@") != 1 {
			t.Errorf("Email %q must contain exactly one @", e)
		}
		parts := strings.Split(e, "@")
		if parts[0] == "" || parts[1] == "" {
			t.Errorf("Email %q must have non-empty local + domain", e)
		}
		if !strings.Contains(parts[1], ".") {
			t.Errorf("Email domain %q must contain a dot", parts[1])
		}
		if len(ph) < 32 {
			t.Errorf("PasswordHash too short: %d chars", len(ph))
		}
	})
}

func TestGen_UserPlaylist(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pl := UserPlaylist(t)
		if pl.Name == "" {
			t.Errorf("Name must be non-empty")
		}
		switch pl.Type {
		case database.UserPlaylistTypeStatic:
			// BookIDs may legitimately be empty (user creates empty playlist first).
			if pl.Query != "" {
				t.Errorf("static playlist should not have Query, got %q", pl.Query)
			}
		case database.UserPlaylistTypeSmart:
			if pl.Query == "" {
				t.Errorf("smart playlist must have non-empty Query")
			}
			if len(pl.BookIDs) != 0 {
				t.Errorf("smart playlist should not have BookIDs, got %d", len(pl.BookIDs))
			}
		default:
			t.Errorf("unknown Type %q", pl.Type)
		}
	})
}

func TestGen_Tag(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tag := Tag(t)
		if len(tag) < 2 || len(tag) > 20 {
			t.Errorf("Tag length out of range [2,20]: %q (len=%d)", tag, len(tag))
		}
		for i, r := range tag {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && r == '-')
			if !ok {
				t.Errorf("Tag %q has invalid rune %q at %d", tag, r, i)
			}
		}
	})
}

func TestGen_OperationChange(t *testing.T) {
	validTypes := map[string]struct{}{
		"file_move": {}, "metadata_update": {}, "tag_write": {},
	}
	rapid.Check(t, func(t *rapid.T) {
		oc := OperationChange(t, "op1", "book1")
		if oc.OperationID != "op1" {
			t.Errorf("OperationID must propagate, got %q", oc.OperationID)
		}
		if oc.BookID != "book1" {
			t.Errorf("BookID must propagate, got %q", oc.BookID)
		}
		if _, ok := validTypes[oc.ChangeType]; !ok {
			t.Errorf("ChangeType %q not in valid set", oc.ChangeType)
		}
		if oc.FieldName == "" {
			t.Errorf("FieldName must be non-empty")
		}
		if oc.CreatedAt.IsZero() {
			t.Errorf("CreatedAt must be set")
		}
		if oc.RevertedAt != nil {
			t.Errorf("RevertedAt must be nil on generation")
		}
	})
}
