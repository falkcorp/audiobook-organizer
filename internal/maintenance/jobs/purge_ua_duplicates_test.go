// file: internal/maintenance/jobs/purge_ua_duplicates_test.go
// version: 1.0.0
// guid: 2c9f5e73-1b48-4d06-a7e3-6d0b8f4c2a95
// last-edited: 2026-08-14

package jobs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// uaFixture builds a root with four cases and a store describing them:
//
//   - DUP:     UA book whose file's INTERIOR matches a real-author twin while
//     the head (tag region) DIFFERS — must be verified and purged.
//   - LONER:   UA book with a unique size — no candidate anywhere; must be
//     kept (this is the ~314 UA-only-survivor class).
//   - COLLIDE: UA book size-identical to a real file but with different
//     interior content — the fixed-bitrate collision; must be kept.
//   - TWIN:    the real-author book — must never be touched.
func uaFixture(t *testing.T) (*database.MockStore, *map[string]bool) {
	t.Helper()
	root := t.TempDir()
	old := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = old })

	const size = 1 << 20 // 1 MiB: probes land at 256/512/768 KiB
	base := bytes.Repeat([]byte{0xAB}, size)

	dupContent := append([]byte(nil), base...)
	copy(dupContent, bytes.Repeat([]byte{0x01}, 4096)) // "UA tag block"
	twinContent := append([]byte(nil), base...)
	copy(twinContent, bytes.Repeat([]byte{0x02}, 4096)) // different tag block
	collideContent := append([]byte(nil), base...)
	copy(collideContent[size/2:], bytes.Repeat([]byte{0xEE}, 4096)) // interior differs
	lonerContent := bytes.Repeat([]byte{0x77}, size/2)              // unique size

	write := func(rel string, content []byte) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dupPath := write("Unknown Author/Dup Book/dup.m4b", dupContent)
	lonerPath := write("Unknown Author/Loner Book/loner.m4b", lonerContent)
	collidePath := write("Unknown Author/Collide Book/collide.m4b", collideContent)
	twinPath := write("Real Author/Dup Book/twin.m4b", twinContent)

	books := []database.BookCore{
		{ID: "DUP", FilePath: dupPath},
		{ID: "LONER", FilePath: lonerPath},
		{ID: "COLLIDE", FilePath: collidePath},
		{ID: "TWIN", FilePath: twinPath},
	}
	bfs := []database.BookFileCore{
		{BookID: "DUP", FilePath: dupPath, FileSize: size},
		{BookID: "LONER", FilePath: lonerPath, FileSize: size / 2},
		{BookID: "COLLIDE", FilePath: collidePath, FileSize: size},
		{BookID: "TWIN", FilePath: twinPath, FileSize: size},
	}

	deleted := map[string]bool{}
	var mu sync.Mutex
	m := &database.MockStore{}
	m.GetAllBooksCoreFunc = func(limit, offset int) ([]database.BookCore, error) { return books, nil }
	m.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) { return bfs, nil }
	m.GetBookByIDFunc = func(id string) (*database.Book, error) { return &database.Book{ID: id}, nil }
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		mu.Lock()
		defer mu.Unlock()
		if b.MarkedForDeletion == nil || !*b.MarkedForDeletion {
			panic("purge wrote a non-soft-delete update: " + id)
		}
		deleted[id] = true
		return b, nil
	}
	return m, &deleted
}

func TestPurgeUADuplicates_DryRunDeletesNothing(t *testing.T) {
	store, deleted := uaFixture(t)
	j := &purgeUADuplicatesJob{}
	if err := j.Run(context.Background(), store, &nopReporter{}, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(*deleted) != 0 {
		t.Fatalf("dry-run soft-deleted %v, want none", *deleted)
	}
}

func TestPurgeUADuplicates_PurgesOnlyInteriorVerified(t *testing.T) {
	store, deleted := uaFixture(t)
	j := &purgeUADuplicatesJob{}
	if err := j.Run(context.Background(), store, &nopReporter{}, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	d := *deleted
	if !d["DUP"] || len(d) != 1 {
		t.Fatalf("soft-deleted %v, want exactly {DUP}", d)
	}
	// The keeps are the audit's non-negotiables:
	if d["LONER"] {
		t.Fatal("purged a UA-only survivor (no twin anywhere) — data loss class")
	}
	if d["COLLIDE"] {
		t.Fatal("purged on a size match with differing interior — content gate failed")
	}
	if d["TWIN"] {
		t.Fatal("purged the real-author twin")
	}
}
