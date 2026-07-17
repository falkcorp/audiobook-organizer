// file: internal/organizer/dataloss_fix_test.go
// version: 1.0.0
// guid: ff38c140-155a-4c69-b3ea-b350a8503066
// last-edited: 2026-07-17

package organizer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// Tests for the 2026-07-17 organizer data-loss fixes (DL-1/DL-2/DL-3):
// two-phase rename rollback + stranded-temp resume, destination-collision
// refusal on every wired move path, and reflink O_EXCL destination creation.

// ---------------------------------------------------------------------------
// safeRename (DL-2)
// ---------------------------------------------------------------------------

func TestSafeRenameDataloss(t *testing.T) {
	t.Run("renames when destination missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.m4b")
		dst := filepath.Join(tmpDir, "dst.m4b")
		if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := safeRename(src, dst); err != nil {
			t.Fatalf("safeRename: %v", err)
		}
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("destination missing after rename: %v", err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("source still exists after rename")
		}
	})

	t.Run("refuses to overwrite existing destination", func(t *testing.T) {
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.m4b")
		dst := filepath.Join(tmpDir, "dst.m4b")
		if err := os.WriteFile(src, []byte("new bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte("existing book"), 0o644); err != nil {
			t.Fatal(err)
		}

		err := safeRename(src, dst)
		if err == nil {
			t.Fatal("expected collision error, got nil")
		}
		if !os.IsExist(err) {
			t.Errorf("expected os.IsExist(err), got: %v", err)
		}
		// Destination bytes must be intact
		data, rerr := os.ReadFile(dst)
		if rerr != nil || string(data) != "existing book" {
			t.Errorf("destination corrupted: %q err=%v", data, rerr)
		}
		// Source must be untouched
		if _, serr := os.Stat(src); serr != nil {
			t.Errorf("source lost: %v", serr)
		}
	})
}

// ---------------------------------------------------------------------------
// RenameFiles rollback + stranded-temp resume (DL-1)
// ---------------------------------------------------------------------------

func TestRenameFilesPhase2CollisionRollsBack(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "old", "book.m4b")
	dst := filepath.Join(tmpDir, "new", "book.m4b")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("mover"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Occupy the final target so phase 2 collides.
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("occupant"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := RenameFiles([]FileRenameEntry{
		{SegmentID: "s1", SourcePath: src, TargetPath: dst},
	})
	if err == nil {
		t.Fatal("expected error for phase-2 collision, got nil")
	}
	if len(result.Succeeded) != 0 {
		t.Errorf("expected 0 succeeded, got %d", len(result.Succeeded))
	}
	// Occupant must be intact — never overwritten.
	data, rerr := os.ReadFile(dst)
	if rerr != nil || string(data) != "occupant" {
		t.Errorf("occupant destroyed: %q err=%v", data, rerr)
	}
	// Source must be rolled back from the temp path — not stranded.
	data, rerr = os.ReadFile(src)
	if rerr != nil || string(data) != "mover" {
		t.Errorf("source not rolled back: %q err=%v", data, rerr)
	}
	if _, terr := os.Stat(dst + tmpRenameSuffix); !os.IsNotExist(terr) {
		t.Error("temp file left behind after rollback")
	}
}

func TestRenameFilesResumesStrandedTemp(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "old", "book.m4b") // does NOT exist
	dst := filepath.Join(tmpDir, "new", "book.m4b")
	tempPath := dst + tmpRenameSuffix
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate a previous run that failed after phase 1: file parked at temp.
	if err := os.WriteFile(tempPath, []byte("stranded"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := RenameFiles([]FileRenameEntry{
		{SegmentID: "s1", SourcePath: src, TargetPath: dst},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("stranded entry bucketed into Skipped (the old lost-forever bug)")
	}
	if len(result.Succeeded) != 1 {
		t.Fatalf("expected 1 succeeded (resumed), got %d", len(result.Succeeded))
	}
	data, rerr := os.ReadFile(dst)
	if rerr != nil || string(data) != "stranded" {
		t.Errorf("resumed file wrong: %q err=%v", data, rerr)
	}
	if _, terr := os.Stat(tempPath); !os.IsNotExist(terr) {
		t.Error("temp file still present after resume")
	}
}

func TestRenameFilesPartialSucceededReportedOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	src1 := filepath.Join(tmpDir, "old", "a.m4b")
	dst1 := filepath.Join(tmpDir, "new", "a.m4b")
	src2 := filepath.Join(tmpDir, "old", "b.m4b")
	dst2 := filepath.Join(tmpDir, "new", "b.m4b")
	if err := os.MkdirAll(filepath.Dir(src1), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, c := range map[string]string{src1: "aaa", src2: "bbb"} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Occupy only the second target: entry 1 completes, entry 2 collides.
	if err := os.MkdirAll(filepath.Dir(dst2), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst2, []byte("occupant"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := RenameFiles([]FileRenameEntry{
		{SegmentID: "s1", SourcePath: src1, TargetPath: dst1},
		{SegmentID: "s2", SourcePath: src2, TargetPath: dst2},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Entry 1 physically moved and must be reported so callers persist its DB path.
	if len(result.Succeeded) != 1 || result.Succeeded[0].SegmentID != "s1" {
		t.Fatalf("expected Succeeded=[s1], got %+v", result.Succeeded)
	}
	if data, rerr := os.ReadFile(dst1); rerr != nil || string(data) != "aaa" {
		t.Errorf("entry 1 not at final path: %q err=%v", data, rerr)
	}
	// Entry 2 rolled back to source; occupant intact.
	if data, rerr := os.ReadFile(src2); rerr != nil || string(data) != "bbb" {
		t.Errorf("entry 2 not rolled back: %q err=%v", data, rerr)
	}
	if data, rerr := os.ReadFile(dst2); rerr != nil || string(data) != "occupant" {
		t.Errorf("occupant destroyed: %q err=%v", data, rerr)
	}
	if _, terr := os.Stat(dst2 + tmpRenameSuffix); !os.IsNotExist(terr) {
		t.Error("temp file left behind after rollback")
	}
}

// ---------------------------------------------------------------------------
// RenameService.moveFile / hardlinkOrCopy collision refusal (DL-2)
// ---------------------------------------------------------------------------

func TestMoveFileRefusesOverwrite(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing book"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := svc.moveFile(src, dst)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if data, rerr := os.ReadFile(dst); rerr != nil || string(data) != "existing book" {
		t.Errorf("destination corrupted: %q err=%v", data, rerr)
	}
	if _, serr := os.Stat(src); serr != nil {
		t.Errorf("source lost: %v", serr)
	}
}

func TestHardlinkOrCopyRefusesOverwrite(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing book"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := svc.hardlinkOrCopy(src, dst)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if data, rerr := os.ReadFile(dst); rerr != nil || string(data) != "existing book" {
		t.Errorf("destination corrupted: %q err=%v", data, rerr)
	}
	// No leftover temp file from the copy fallback.
	if _, terr := os.Stat(dst + ".tmp"); !os.IsNotExist(terr) {
		t.Error("copy temp file left behind")
	}
}

// ---------------------------------------------------------------------------
// Organizer.copyFile finalize collision refusal (DL-3 companion)
// ---------------------------------------------------------------------------

func TestCopyFileRefusesOverwrite(t *testing.T) {
	o := &Organizer{}
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing book"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := o.copyFile(src, dst)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if !os.IsExist(err) {
		t.Errorf("expected os.IsExist(err) for organize-pool race recovery, got: %v", err)
	}
	if data, rerr := os.ReadFile(dst); rerr != nil || string(data) != "existing book" {
		t.Errorf("destination corrupted: %q err=%v", data, rerr)
	}
}
