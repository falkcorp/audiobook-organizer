// file: core/backup/compat_test.go
// version: 1.0.0
// guid: 4f7a1c92-3e58-4b06-9d1a-7c2e8b5f0a64
// last-edited: 2026-11-12

package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/storage"
)

// wrapInspector decorates the fixture's inspector: gens are the derived items'
// generations "in the checkpoint", fail replaces the open.
type wrapInspector struct {
	backup.Inspector
	gens map[string]string
	fail error
}

func (w wrapInspector) Inspect(ctx context.Context, dir string) (backup.StoreInfo, error) {
	if w.fail != nil {
		return backup.StoreInfo{}, w.fail
	}
	info, err := w.Inspector.Inspect(ctx, dir)
	if err == nil && w.gens != nil {
		info.Generations = w.gens
	}
	return info, err
}

func TestRestoreBelowUpgradeFloorRefusedAtStaging(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	owners := append([]storage.OwnerSpec(nil), e.owners...)
	owners[1].Spec.UpgradeFloor = &storage.UpgradeFloor{Owner: gothamOwner, Version: 99, InstallFirst: "v0.9.0"}
	e.opts.Owners = owners
	e.rebuild()
	for _, host := range []string{"in place", "host loss"} {
		if host == "host loss" {
			e.opts.Store = nil // a fresh host: there is no live store to protect or to disagree
			e.rebuild()
		}
		_, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{})
		var uf *storage.ErrUpgradeFloor
		if !errors.As(err, &uf) || uf.Owner != gothamOwner || uf.Floor != 99 || uf.InstallFirst != "v0.9.0" {
			t.Fatalf("%s: err = %v, want *storage.ErrUpgradeFloor naming v0.9.0", host, err)
		}
		if len(e.stager.staged) != 0 {
			t.Fatalf("%s: an intent was created below the floor", host)
		}
		// Refused before extraction: nothing was written for the restore.
		if _, err := os.Stat(filepath.Join(e.dbParent, ".mediacore-restore")); !os.IsNotExist(err) {
			t.Fatalf("%s: staging was created before the floor was checked: %v", host, err)
		}
	}
}

func TestRestoreRefusesBreakingStepAboveTheBuild(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	e.opts.Inspector = wrapInspector{Inspector: e.opts.Inspector, fail: storage.ErrKeyspaceTooNew}
	e.rebuild()
	_, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{})
	if !errors.Is(err, backup.ErrBackupTooNew) || !errors.Is(err, storage.ErrKeyspaceTooNew) {
		t.Fatalf("err = %v, want ErrBackupTooNew wrapping ErrKeyspaceTooNew", err)
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("staged")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".mediacore-restore")); len(ents) != 0 {
		t.Fatalf("the refused restore left %d staging entries", len(ents))
	}
}

func TestDerivedItemsGenerationChecked(t *testing.T) {
	ctx := context.Background()
	derivedDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(derivedDir, "segment.idx"), []byte("the penguin index"), 0o600); err != nil {
		t.Fatal(err)
	}
	art := func() []backup.Artifact {
		return []backup.Artifact{{Name: "books.search", Owner: "books", Class: backup.Derived, Dir: derivedDir}}
	}
	cases := []struct {
		name      string
		include   bool
		cpGen     map[string]string
		live      func(context.Context, string) (string, error)
		wantItem  bool
		wantStale bool
	}{
		{"source untouched", true, map[string]string{"books.search": "g1"}, func(context.Context, string) (string, error) { return "g1", nil }, true, false},
		{"source touched between the checkpoint and the capture", true, map[string]string{"books.search": "g1"}, func(context.Context, string) (string, error) { return "g2", nil }, true, true},
		{"the live generation cannot be read", true, map[string]string{"books.search": "g1"}, func(context.Context, string) (string, error) { return "", errors.New("index busy") }, true, true},
		{"no way to read the live generation", true, map[string]string{"books.search": "g1"}, nil, true, true},
		{"the checkpoint has no generation for it", true, nil, func(context.Context, string) (string, error) { return "g1", nil }, true, true},
		{"not asked for", false, map[string]string{"books.search": "g1"}, func(context.Context, string) (string, error) { return "g1", nil }, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.opts.Artifacts = art
			e.opts.DerivedGeneration = c.live
			e.opts.Inspector = wrapInspector{Inspector: e.opts.Inspector, gens: c.cpGen}
			e.rebuild()
			a, err := e.svc.Create(ctx, backup.CreateOptions{IncludeDerived: c.include})
			if err != nil {
				t.Fatal(err)
			}
			got, ok := a.Manifest.Derived["books.search"]
			if ok != c.wantItem {
				t.Fatalf("derived record present = %v, want %v (%+v)", ok, c.wantItem, a.Manifest.Derived)
			}
			if !c.wantItem {
				for _, en := range a.Manifest.Entries {
					if filepath.Base(en.Path) == "segment.idx" {
						t.Fatalf("a derived item nobody asked for was archived: %s", en.Path)
					}
				}
				return
			}
			if got.Stale != c.wantStale {
				t.Fatalf("Stale = %v, want %v", got.Stale, c.wantStale)
			}
			if c.cpGen != nil && got.Generation != "g1" {
				t.Fatalf("Generation = %q, want the checkpoint's g1", got.Generation)
			}
		})
	}
}
