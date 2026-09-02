// file: internal/metafetch/virtual_bookfile_test.go
// version: 1.0.0
// guid: 3f1e9c72-8b4d-4a6e-9d05-7c2b1a8e4f60
// last-edited: 2026-09-02

package metafetch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A row-less single-file book's virtual entry must carry the book row's size:
// the organizer refuses to resume a stranded rename temp it cannot size-check,
// so a size-less virtual entry parked every such rename forever (#3051 review).
func TestVirtualBookFiles_CarriesBookFileSize(t *testing.T) {
	size := int64(4096)
	got := virtualBookFiles("b1", &database.Book{FilePath: "/lib/A/T/T.m4b", FileSize: &size})
	if len(got) != 1 {
		t.Fatalf("expected 1 virtual entry, got %d", len(got))
	}
	if got[0].ID != "virtual-b1" || got[0].BookID != "b1" || got[0].Format != "m4b" {
		t.Errorf("virtual entry shape wrong: %+v", got[0])
	}
	if got[0].FileSize != size {
		t.Errorf("FileSize = %d, want %d (book row size dropped)", got[0].FileSize, size)
	}
}

func TestVirtualBookFiles_NoSizeStillBuildsEntry(t *testing.T) {
	got := virtualBookFiles("b1", &database.Book{FilePath: "/lib/A/T/T.mp3"})
	if len(got) != 1 || got[0].FileSize != 0 {
		t.Fatalf("expected one size-less entry, got %+v", got)
	}
}

func TestVirtualBookFiles_DirectoryOrEmptyPathIsNil(t *testing.T) {
	if got := virtualBookFiles("b1", &database.Book{FilePath: "/lib/A/T"}); got != nil {
		t.Errorf("directory FilePath produced %+v", got)
	}
	if got := virtualBookFiles("b1", &database.Book{}); got != nil {
		t.Errorf("empty FilePath produced %+v", got)
	}
	if got := virtualBookFiles("b1", nil); got != nil {
		t.Errorf("nil book produced %+v", got)
	}
}

// End to end through RenameFiles: the stranded temp of a row-less book is
// resumed when the book row records its size, and refused (left in place,
// reported) when it does not.
func TestVirtualBookFiles_StrandedTempResumesOnlyWithSize(t *testing.T) {
	for _, tc := range []struct {
		name       string
		size       *int64
		wantResume bool
	}{
		{"with size", func() *int64 { n := int64(len("stranded")); return &n }(), true},
		{"without size", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			src := filepath.Join(tmp, "old", "book.m4b") // gone
			dst := filepath.Join(tmp, "new", "book.m4b")
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			temp := dst + tmpRenameSuffix
			if err := os.WriteFile(temp, []byte("stranded"), 0o644); err != nil {
				t.Fatal(err)
			}
			files := virtualBookFiles("b1", &database.Book{FilePath: src, FileSize: tc.size})
			// ComputeTargetPaths copies BookFile.FileSize into ExpectedSize
			// (pinned by mutant M14); mirror that here without a store.
			result, err := RenameFiles([]FileRenameEntry{{
				SegmentID: files[0].ID, SourcePath: files[0].FilePath,
				TargetPath: dst, ExpectedSize: files[0].FileSize,
			}})
			if tc.wantResume {
				if err != nil || len(result.Succeeded) != 1 {
					t.Fatalf("expected resume: err=%v succeeded=%d", err, len(result.Succeeded))
				}
				if _, serr := os.Stat(dst); serr != nil {
					t.Errorf("resumed file missing: %v", serr)
				}
				return
			}
			if err == nil {
				t.Fatal("expected refusal without a recorded size")
			}
			if _, serr := os.Stat(temp); serr != nil {
				t.Errorf("refused temp must stay parked: %v", serr)
			}
		})
	}
}
