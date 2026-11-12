// file: core/backup/catalogue_test.go
// version: 1.0.0
// guid: 2e8b4c90-71d3-4a5f-9c16-0b5d3a7e1f48
// last-edited: 2026-11-12

package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

func has(names []string, sub string) bool {
	for _, n := range names {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestCatalogueDropsOrphans(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	b := e.create("manual")
	// An archive deleted outside the service leaves its sidecars behind.
	if err := os.Remove(e.path(a)); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("List = %v, want only %s", list, b.ID)
	}
	if has(e.names(), a.Name) {
		t.Fatalf("the orphaned sidecars of %s were kept: %v", a.Name, e.names())
	}
	if !has(e.names(), b.Name) {
		t.Fatalf("the live archive's files vanished: %v", e.names())
	}
}

func TestCatalogueSurvivesRestore(t *testing.T) {
	e := newEnv(t)
	e.settings.MaxCount = 1
	a := e.create("manual")
	e.settings.MaxCount = 10
	b := e.create("manual")
	c := e.create("manual")
	if _, err := e.svc.StageRestore(context.Background(), a.ID, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	list, _ := e.svc.List(context.Background())
	if len(list) != 3 {
		t.Fatalf("after staging a restore of the oldest archive, List has %d archives, want 3", len(list))
	}
	// Retention at count 1: the newest stays by count, the staged restore's source
	// by floor, and the middle one goes.
	e.settings.MaxCount = 1
	if err := e.svc.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := e.names()
	if !has(got, a.Name) || has(got, b.Name) || !has(got, c.Name) {
		t.Fatalf("after prune: %v; want %s and %s kept, %s gone", got, a.Name, c.Name, b.Name)
	}
	// A staged restore's archive cannot be deleted from under it either.
	if err := e.svc.Delete(context.Background(), a.ID); !errors.Is(err, backup.ErrProtectedByFloor) {
		t.Fatalf("Delete of the staged restore's archive: %v, want ErrProtectedByFloor", err)
	}
}

func TestJournalNamePerApp(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"gotham_revocations.journal", "arkham_revocations.journal"} {
		if err := os.MkdirAll(e.backupDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.backupDir, p), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.settings.MaxCount = 1
	e.create("manual")
	e.create("manual")
	other := e.opts
	other.AppID, other.Prefix = "arkham", "arkham_"
	osvc, err := backup.New(other)
	if err != nil {
		t.Fatal(err)
	}
	oa, err := osvc.Create(context.Background(), backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The first gotham archive is the newest verified one when the second is
	// created, so retention at count 1 keeps both; the point here is that each
	// app lists only its own.
	mine, _ := e.svc.List(context.Background())
	theirs, _ := osvc.List(context.Background())
	if len(mine) != 2 || len(theirs) != 1 || theirs[0].ID != oa.ID {
		t.Fatalf("each app lists only its own archives: %d and %d", len(mine), len(theirs))
	}
	for _, a := range mine {
		if !strings.HasPrefix(a.Name, "gotham_") {
			t.Fatalf("the gotham catalogue lists %s", a.Name)
		}
	}
	// Pruning either app removes neither journal and none of the other's files.
	if err := e.svc.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := osvc.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"gotham_revocations.journal", "arkham_revocations.journal"} {
		if _, err := os.Stat(filepath.Join(e.backupDir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if mine, _ = e.svc.List(context.Background()); len(mine) != 1 {
		t.Fatalf("this app's retention left %d archives, want 1", len(mine))
	}
	if theirs, _ = osvc.List(context.Background()); len(theirs) != 1 {
		t.Fatalf("the other app's archive was pruned by this app's retention")
	}
}

func TestRetentionFloors(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	b := e.create("manual")
	c := e.create("manual") // verified at the open level, like a and b
	e.settings.VerifyAfterCreate = false
	d := e.create("manual") // newest, never verified
	e.settings.MaxCount = 1
	if err := e.svc.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	got := e.names()
	// Floor 1: an unverified newer archive does not protect the last good one, and
	// the last good one is kept although it is not within the count.
	if !has(got, c.Name) || has(got, d.Name) || has(got, a.Name) || has(got, b.Name) {
		t.Fatalf("after prune at count 1: %v; want only %s (the newest verified archive)", got, c.Name)
	}
	if err := e.svc.Delete(ctx, c.ID); !errors.Is(err, backup.ErrProtectedByFloor) {
		t.Fatalf("Delete of the newest verified archive: %v, want ErrProtectedByFloor", err)
	}
	// Once a newer archive is verified it becomes the floor, and the old one is free.
	e.settings.MaxCount = 10
	f := e.create("manual")
	if err := e.svc.Delete(ctx, c.ID); !errors.Is(err, backup.ErrProtectedByFloor) {
		t.Fatalf("an unverified newer archive released the floor: %v", err)
	}
	if _, err := e.svc.Verify(ctx, f.ID, backup.VerifyEntries); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete of a no-longer-floor archive: %v", err)
	}
	if err := e.svc.Delete(ctx, f.ID); !errors.Is(err, backup.ErrProtectedByFloor) {
		t.Fatalf("Delete of the new floor: %v", err)
	}
	// A failed verification is not a floor.
	e.settings.VerifyAfterCreate = true
	g := e.create("manual")
	if err := os.WriteFile(e.path(g)+backup.ChecksumSidecar, []byte(strings.Repeat("0", 64)+"  x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Verify(ctx, g.ID, backup.VerifyChecksum); err == nil {
		t.Fatal("a wrong checksum verified")
	}
	if err := e.svc.Delete(ctx, g.ID); err != nil {
		t.Fatalf("an archive whose last verification failed is protected: %v", err)
	}
	if err := e.svc.Delete(ctx, "01ZZZZZZZZZZZZZZZZZZZZZZZZ"); !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("Delete of an unknown archive: %v", err)
	}
}

func TestRetentionBounds(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.settings.VerifyAfterCreate = false
	var as []backup.Archive
	for range 4 {
		as = append(as, e.create("manual"))
	}
	// Age: archives older than MaxAge go, newest-verified aside (none is verified here).
	e.settings.MaxCount = 10
	e.settings.MaxAge = 5 * time.Minute
	e.opts.Clock = func() time.Time { return as[3].CreatedAt.Add(time.Minute) }
	e.rebuild()
	if err := e.svc.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(e.names()); n == 0 || has(e.names(), as[0].Name) {
		t.Fatalf("after an age prune: %v", e.names())
	}
	// Bytes: a budget of one byte keeps nothing unprotected.
	e.settings.MaxAge, e.settings.MaxTotalBytes = 0, 1
	if err := e.svc.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.svc.List(ctx); len(list) != 0 {
		t.Fatalf("a one-byte budget left %d archives (none is protected)", len(list))
	}
}

// dirBytes is the size of the files in dir whose names contain sub.
func dirBytes(dir, sub string) (n uint64) {
	ents, _ := os.ReadDir(dir)
	for _, d := range ents {
		if strings.Contains(d.Name(), sub) {
			if fi, err := d.Info(); err == nil {
				n += uint64(fi.Size())
			}
		}
	}
	return n
}

// TestRetentionRunsBeforeSpaceCheck is the 2026-08-29 stuck-disk regression: a
// disk that is full of old archives must be pruned before the space check, or
// the backup that would free it can never run.
func TestRetentionRunsBeforeSpaceCheck(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.settings.VerifyAfterCreate = false
	a := e.create("manual")
	b := e.create("manual")
	need := uint64(1)
	// The disk's free space is a fixed limit less what the backup directory holds.
	// Sized so it is short with a and b present and enough once a is gone.
	tA, tB := dirBytes(e.backupDir, a.Name), dirBytes(e.backupDir, b.Name)
	var est uint64
	e.opts.Disk = backup.Disk{Avail: func(p string) (uint64, error) {
		used := dirBytes(e.backupDir, "gotham_")
		limit := est + need + tB + tA/2
		if used > limit {
			return 0, nil
		}
		return limit - used, nil
	}}
	est = dbSize(t, e.st.Path())
	e.rebuild()
	e.settings.MaxCount = 1
	c, err := e.svc.Create(ctx, backup.CreateOptions{})
	if err != nil {
		t.Fatalf("Create on a disk that retention can free: %v", err)
	}
	if has(e.names(), a.Name) {
		t.Fatal("the oldest archive was not pruned before the space check")
	}
	// With nothing prunable (every archive protected by count) it is refused.
	e.settings.MaxCount = 10
	e.opts.Disk = backup.Disk{Avail: func(string) (uint64, error) { return 0, nil }}
	e.rebuild()
	if _, err := e.svc.Create(ctx, backup.CreateOptions{}); !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("err = %v, want ErrInsufficientSpace", err)
	}
	_ = c
}

func dbSize(t *testing.T, dir string) uint64 {
	t.Helper()
	var n uint64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += uint64(fi.Size())
			}
		}
		return nil
	})
	return n
}

// TestBackupTaskExclusive is the service's half of the one backup resource:
// while Create holds it, prune, stage, cancel, delete and rewrap are refused
// with ErrBackupInProgress, and List and Verify are not blocked.
func TestBackupTaskExclusive(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	first := e.create("manual")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.opts.Artifacts = func() []backup.Artifact {
		return []backup.Artifact{{Name: "books.hold", Owner: "books", Class: backup.SystemOfRecord, Consistency: backup.AppendOnly,
			Snapshot: func(ctx context.Context, w backup.ArtifactWriter) error {
				once.Do(func() { close(entered) })
				<-release
				f, err := w.Create("hold.txt", 0o600)
				if err != nil {
					return err
				}
				_, _ = f.Write([]byte("held"))
				return f.Close()
			}}}
	}
	e.rebuild()
	done := make(chan error, 1)
	go func() { _, err := e.svc.Create(ctx, backup.CreateOptions{}); done <- err }()
	<-entered
	refused := map[string]error{
		"Create": func() error { _, err := e.svc.Create(ctx, backup.CreateOptions{}); return err }(),
		"Prune":  e.svc.Prune(ctx),
		"Delete": e.svc.Delete(ctx, first.ID),
		"Cancel": e.svc.CancelRestore(ctx),
		"Stage":  func() error { _, err := e.svc.StageRestore(ctx, first.ID, backup.RestoreOptions{}); return err }(),
		"Rewrap": func() error { _, err := e.svc.Rewrap(ctx, first.ID); return err }(),
		"Revert": func() error { _, err := e.svc.RevertLastRestore(ctx, backup.RestoreOptions{}); return err }(),
	}
	for name, err := range refused {
		if !errors.Is(err, backup.ErrBackupInProgress) {
			t.Errorf("%s while Create holds the resource: %v, want ErrBackupInProgress", name, err)
		}
	}
	if _, err := e.svc.List(ctx); err != nil {
		t.Errorf("List was blocked: %v", err)
	}
	if _, err := e.svc.Verify(ctx, first.ID, backup.VerifyChecksum); err != nil {
		t.Errorf("Verify of another archive was blocked: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Prune(ctx); err != nil {
		t.Fatalf("the resource was not released: %v", err)
	}
}

func TestCreateCancelRemovesPartial(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.opts.Artifacts = func() []backup.Artifact {
		return []backup.Artifact{{Name: "books.cancel", Owner: "books", Class: backup.SystemOfRecord, Consistency: backup.AppendOnly,
			Snapshot: func(context.Context, backup.ArtifactWriter) error { cancel(); return ctx.Err() }}}
	}
	e.rebuild()
	if _, err := e.svc.Create(ctx, backup.CreateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("a canceled backup left %v", n)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".backup-staging")); len(ents) != 0 {
		t.Fatalf("a canceled backup left %d staging entries", len(ents))
	}
	// A canceled run is not a failed verification and not a created archive.
	e.opts.Artifacts = nil
	e.rebuild()
	if _, err := e.svc.Create(context.Background(), backup.CreateOptions{}); err != nil {
		t.Fatalf("a Create after a canceled one: %v", err)
	}
}

var _ = restorejournal.PendingName
