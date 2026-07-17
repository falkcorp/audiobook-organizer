// file: internal/metafetch/file_pipeline_dataloss_test.go
// version: 1.0.0
// guid: 6f1ed9f6-e6b5-47cb-9843-94343fe2e7eb
// last-edited: 2026-07-17

package metafetch

import (
	"os"
	"path/filepath"
	"testing"
)

// Mirrors internal/organizer/dataloss_fix_test.go for this package's twin
// RenameFiles implementation (the one actually wired into the apply /
// write-back paths).

func TestMetafetchRenameFilesPhase2CollisionRollsBack(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "old", "book.m4b")
	dst := filepath.Join(tmpDir, "new", "book.m4b")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("mover"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if data, rerr := os.ReadFile(dst); rerr != nil || string(data) != "occupant" {
		t.Errorf("occupant destroyed: %q err=%v", data, rerr)
	}
	if data, rerr := os.ReadFile(src); rerr != nil || string(data) != "mover" {
		t.Errorf("source not rolled back: %q err=%v", data, rerr)
	}
	if _, terr := os.Stat(dst + tmpRenameSuffix); !os.IsNotExist(terr) {
		t.Error("temp file left behind after rollback")
	}
}

func TestMetafetchRenameFilesResumesStrandedTemp(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "old", "book.m4b") // does NOT exist
	dst := filepath.Join(tmpDir, "new", "book.m4b")
	tempPath := dst + tmpRenameSuffix
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
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
		t.Error("stranded entry bucketed into Skipped (the old lost-forever bug)")
	}
	if len(result.Succeeded) != 1 {
		t.Fatalf("expected 1 succeeded (resumed), got %d", len(result.Succeeded))
	}
	if data, rerr := os.ReadFile(dst); rerr != nil || string(data) != "stranded" {
		t.Errorf("resumed file wrong: %q err=%v", data, rerr)
	}
	if _, terr := os.Stat(tempPath); !os.IsNotExist(terr) {
		t.Error("temp file still present after resume")
	}
}
