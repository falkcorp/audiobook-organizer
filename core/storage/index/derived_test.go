// file: core/storage/index/derived_test.go
// version: 1.0.0
// guid: 6ba1786a-8623-4c8d-88c0-70f73786e8cd
// last-edited: 2026-11-11

package index_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index/indextest"
)

func epochOf(g storage.Generation) uint64 { return binary.BigEndian.Uint64(g[:8]) }
func touchOf(g storage.Generation) uint64 { return binary.BigEndian.Uint64(g[8:]) }

// batmobile is a module's warm-start artifact over its own storage: a fake
// hook pair, written the way a real one is (read Current, export, StampArtifact
// in Snapshot; ArtifactAccepted AND the owner's own truth count in Load).
type batmobile struct {
	f   *indextest.Fixture
	der storage.Derived
}

func (m batmobile) count(ctx context.Context) int {
	n := 0
	for _, err := range m.f.Module.ScanPrefix(ctx, []byte("car/")) {
		if err != nil {
			panic(err)
		}
		n++
	}
	return n
}

// put adds or removes a car in one batch that touches the generation, as a
// module's write path must.
func (m batmobile) put(ctx context.Context, id string, present bool) error {
	b := m.f.Module.NewBatch()
	defer b.Close()
	key := []byte("car/" + id)
	var err error
	if present {
		err = b.Set(key, []byte("1"))
	} else {
		err = b.Delete(key)
	}
	if err != nil {
		return err
	}
	if err := m.der.Touch(ctx, b); err != nil {
		return err
	}
	return b.Commit(ctx, storage.Sync)
}

// putBlind changes the source WITHOUT touching: the defect the generation rule
// is conditional on (HEAD's writers), used only to show what the rule assumes.
func (m batmobile) putBlind(ctx context.Context, id string) error {
	return m.f.Module.Set(ctx, []byte("car/"+id), []byte("1"))
}

func (m batmobile) hooks(truth *int) storage.DerivedHooks {
	return storage.DerivedHooks{
		Snapshot: func(ctx context.Context, dir string, gen storage.Generation) error {
			if err := os.WriteFile(filepath.Join(dir, "cars"), []byte(fmt.Sprint(m.count(ctx))), 0o600); err != nil {
				return err
			}
			return storage.StampArtifact(dir, gen)
		},
		Load: func(ctx context.Context, dir string, current storage.Generation) (bool, error) {
			ok, err := storage.ArtifactAccepted(dir, current)
			if err != nil || !ok {
				return false, err
			}
			raw, err := os.ReadFile(filepath.Join(dir, "cars"))
			if err != nil {
				return false, err
			}
			return string(raw) == fmt.Sprint(*truth), nil // the owner's own check: a truth count
		},
	}
}

func (m batmobile) snapshot(t *testing.T, h storage.DerivedHooks, dir string) {
	t.Helper()
	gen, err := m.der.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Snapshot(t.Context(), dir, gen); err != nil {
		t.Fatal(err)
	}
}

func (m batmobile) load(t *testing.T, h storage.DerivedHooks, dir string) bool {
	t.Helper()
	cur, err := m.der.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ok, err := h.Load(t.Context(), dir, cur)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestDerivedGenerationRejectsStaleArtifact is I2: an artifact is never falsely
// accepted. An add and a delete that cancel in a count are still rejected, and
// an artifact from another store generation is rejected.
func TestDerivedGenerationRejectsStaleArtifact(t *testing.T) {
	t.Run("an unchanged source accepts its own artifact", func(t *testing.T) {
		ctx := t.Context()
		f := openMem(t)
		m := batmobile{f, f.Derived(indextest.Signal)}
		for _, id := range []string{"1966", "1989"} {
			if err := m.put(ctx, id, true); err != nil {
				t.Fatal(err)
			}
		}
		truth := m.count(ctx)
		h, dir := m.hooks(&truth), t.TempDir()
		m.snapshot(t, h, dir)
		if !m.load(t, h, dir) {
			t.Fatal("the positive control: an artifact of an unchanged source was refused")
		}
	})

	t.Run("an add and a delete that cancel in a count are rejected", func(t *testing.T) {
		ctx := t.Context()
		f := openMem(t)
		m := batmobile{f, f.Derived(indextest.Signal)}
		for _, id := range []string{"1966", "1989"} {
			if err := m.put(ctx, id, true); err != nil {
				t.Fatal(err)
			}
		}
		truth := m.count(ctx)
		h, dir := m.hooks(&truth), t.TempDir()
		m.snapshot(t, h, dir)

		if err := m.put(ctx, "1966", false); err != nil { // delete
			t.Fatal(err)
		}
		if err := m.put(ctx, "1997", true); err != nil { // add
			t.Fatal(err)
		}
		truth = m.count(ctx)
		if truth != 2 {
			t.Fatalf("the cancelling pair must leave the count at 2, got %d", truth)
		}
		if m.load(t, h, dir) {
			t.Fatal("an artifact missing a document and holding a deleted one was accepted because the count matched")
		}
		// The count alone WOULD have passed: the owner's check cannot catch this.
		raw, _ := os.ReadFile(filepath.Join(dir, "cars"))
		if string(raw) != fmt.Sprint(truth) {
			t.Fatalf("the scenario is not a cancelling pair: artifact %s vs truth %d", raw, truth)
		}
	})

	t.Run("a touch that commits during the export rejects the artifact", func(t *testing.T) {
		ctx := t.Context()
		f := openMem(t)
		m := batmobile{f, f.Derived(indextest.Signal)}
		if err := m.put(ctx, "1966", true); err != nil {
			t.Fatal(err)
		}
		truth := m.count(ctx)
		h, dir := m.hooks(&truth), t.TempDir()
		gen, err := m.der.Current(ctx) // Snapshot's contract: read Current BEFORE exporting
		if err != nil {
			t.Fatal(err)
		}
		if err := m.put(ctx, "1989", true); err != nil { // commits mid-export
			t.Fatal(err)
		}
		truth = m.count(ctx)
		if err := h.Snapshot(ctx, dir, gen); err != nil { // the export saw both cars
			t.Fatal(err)
		}
		if m.load(t, h, dir) {
			t.Fatal("an artifact stamped with the pre-export generation was accepted after a touch committed during the export")
		}
	})

	t.Run("an artifact from another store generation is rejected", func(t *testing.T) {
		ctx := t.Context()
		base := t.TempDir()
		pathA := filepath.Join(base, "a", "main.pebble")
		pathB := filepath.Join(base, "b", "main.pebble")
		if err := os.MkdirAll(filepath.Dir(pathA), 0o700); err != nil {
			t.Fatal(err)
		}
		fa := indextest.Open(t, storage.FS{}, pathA)
		a := batmobile{fa, fa.Derived(indextest.Signal)}
		for _, id := range []string{"1966", "1989"} {
			if err := a.put(ctx, id, true); err != nil {
				t.Fatal(err)
			}
		}
		truth := a.count(ctx)
		h, dir := a.hooks(&truth), t.TempDir()
		a.snapshot(t, h, dir)
		genA, _ := a.der.Current(ctx)
		if !a.load(t, h, dir) {
			t.Fatal("the positive control: the artifact was refused by its own store")
		}
		if err := fa.Close(); err != nil {
			t.Fatal(err)
		}

		// The store is relocated: byte-identical content in another directory,
		// which §5S.3.7 gives a new store generation. Its sequence high-water
		// mark and generation key are the same, so the touch value alone
		// cannot tell the two stores apart.
		if err := copyTree(pathA, pathB); err != nil {
			t.Fatal(err)
		}
		fb := indextest.Open(t, storage.FS{}, pathB)
		b := batmobile{fb, fb.Derived(indextest.Signal)}
		genB, err := b.der.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if touchOf(genB) != touchOf(genA) {
			t.Fatalf("the scenario needs the same touch value in both stores: %d vs %d", touchOf(genA), touchOf(genB))
		}
		if epochOf(genB) <= epochOf(genA) {
			t.Fatalf("a relocated store must have a new store generation: %d then %d", epochOf(genA), epochOf(genB))
		}
		truthB := b.count(ctx)
		if truthB != truth {
			t.Fatalf("the copy holds %d cars, want %d", truthB, truth)
		}
		hb := b.hooks(&truthB)
		if b.load(t, hb, dir) {
			t.Fatal("an artifact recorded in another store generation was accepted: the same touch value named two stores")
		}
	})

	t.Run("an artifact without a usable stamp is rejected", func(t *testing.T) {
		ctx := t.Context()
		f := openMem(t)
		m := batmobile{f, f.Derived(indextest.Signal)}
		if err := m.put(ctx, "1966", true); err != nil {
			t.Fatal(err)
		}
		truth := m.count(ctx)
		h := m.hooks(&truth)
		cur, _ := m.der.Current(ctx)

		unstamped := t.TempDir()
		if err := os.WriteFile(filepath.Join(unstamped, "cars"), []byte("1"), 0o600); err != nil {
			t.Fatal(err)
		}
		if ok, err := h.Load(ctx, unstamped, cur); ok || err != nil {
			t.Fatalf("an unstamped artifact: %v, %v", ok, err)
		}
		for name, content := range map[string]string{
			"empty":     "",
			"garbage":   "not hex\n",
			"short":     "00ff\n",
			"all zeros": storage.Generation{}.String() + "\n",
		} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "cars"), []byte("1"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, storage.ArtifactGenerationFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if ok, err := h.Load(ctx, dir, cur); ok || err != nil {
				t.Fatalf("%s stamp: %v, %v", name, ok, err)
			}
			// A zero stamp equals a zero current, so only the two zero checks
			// together keep an unstamped artifact against an unreadable store
			// generation from being accepted.
			if ok, err := storage.ArtifactAccepted(dir, storage.Generation{}); ok || err != nil {
				t.Fatalf("%s stamp against a zero current: %v, %v", name, ok, err)
			}
		}
		if ok, _ := storage.ArtifactAccepted(unstamped, storage.Generation{}); ok {
			t.Fatal("a zero current generation accepted something")
		}
		if err := storage.StampArtifact(t.TempDir(), storage.Generation{}); err == nil {
			t.Fatal("the zero generation was stamped")
		}
	})
}

// TestDerivedTouchValuesAreUnique: every Touch leaves a value no earlier Touch
// left, which is what makes "any later touch differs from the recorded one"
// true, and a Touch is invisible until its batch commits.
func TestDerivedTouchValuesAreUnique(t *testing.T) {
	ctx := t.Context()
	f := openMem(t)
	der := f.Derived(indextest.Signal)
	g0, err := der.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if touchOf(g0) != 0 || epochOf(g0) == 0 {
		t.Fatalf("a never-touched index is (store generation, 0): epoch %d touch %d", epochOf(g0), touchOf(g0))
	}
	seen := map[storage.Generation]bool{g0: true}
	last := g0
	for i := range 50 {
		b := f.Module.NewBatch()
		if err := der.Touch(ctx, b); err != nil {
			t.Fatal(err)
		}
		if g, _ := der.Current(ctx); g != last {
			t.Fatalf("touch %d visible before commit", i)
		}
		if err := b.Commit(ctx, storage.NoSync); err != nil {
			t.Fatal(err)
		}
		g, err := der.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if seen[g] {
			t.Fatalf("touch %d left a generation an earlier state already had: %s", i, g)
		}
		seen[g] = true
		last = g
	}
	// Two indexes draw from one sequence: their touch values never coincide.
	other := f.Derived(indextest.Cave)
	b := f.Module.NewBatch()
	if err := other.Touch(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.NoSync); err != nil {
		t.Fatal(err)
	}
	gs, _ := der.Current(ctx)
	go2, _ := other.Current(ctx)
	if touchOf(gs) == touchOf(go2) {
		t.Fatalf("two indexes share the touch value %d", touchOf(gs))
	}
}

func TestDerivedRefusals(t *testing.T) {
	ctx := t.Context()
	f := openMem(t)
	if _, err := f.DB.Derived(indextest.Module, "joker"); !errors.Is(err, storage.ErrDerivedUndeclared) {
		t.Fatalf("an undeclared index: %v", err)
	}
	if _, err := f.DB.Derived("nobody", indextest.Signal); !errors.Is(err, storage.ErrDerivedUndeclared) {
		t.Fatalf("an unregistered module: %v", err)
	}
	if _, err := f.DB.Derived("core/index", indextest.Signal); !errors.Is(err, storage.ErrDerivedUndeclared) {
		t.Fatalf("a core owner: %v", err)
	}
	var zero storage.Derived
	if err := zero.Touch(ctx, f.Module.NewBatch()); !errors.Is(err, storage.ErrInvalidKeyspace) {
		t.Fatalf("zero Touch: %v", err)
	}
	if _, err := zero.Current(ctx); !errors.Is(err, storage.ErrInvalidKeyspace) {
		t.Fatalf("zero Current: %v", err)
	}
	if err := f.Derived(indextest.Signal).Touch(ctx, nil); err == nil {
		t.Fatal("Touch with a nil batch")
	}
	for _, d := range []storage.DerivedIndex{{Name: "Bad Name", Version: "1"}, {Name: "ok", Version: " "}} {
		if d.Validate() == nil {
			t.Fatalf("%+v validated", d)
		}
	}
	if err := (storage.DerivedIndex{Name: "hnsw", Version: "3"}).Validate(); err != nil {
		t.Fatal(err)
	}
	g, _ := f.Derived(indextest.Signal).Current(ctx)
	back, err := storage.ParseGeneration(g.String())
	if err != nil || back != g {
		t.Fatalf("round trip %v %v", back, err)
	}
	if _, err := storage.ParseGeneration("zz"); err == nil {
		t.Fatal("ParseGeneration accepted garbage")
	}
}

// TestDerivedGenerationRuleAssumesEveryWriteTouches documents the rule's one
// proviso (§5S.3.9): a write that changes the source without a Touch is not
// seen by the generation, which is why the owner's own check stays.
func TestDerivedGenerationRuleAssumesEveryWriteTouches(t *testing.T) {
	ctx := t.Context()
	f := openMem(t)
	m := batmobile{f, f.Derived(indextest.Signal)}
	if err := m.put(ctx, "1966", true); err != nil {
		t.Fatal(err)
	}
	truth := m.count(ctx)
	h, dir := m.hooks(&truth), t.TempDir()
	m.snapshot(t, h, dir)
	if err := m.putBlind(ctx, "1989"); err != nil {
		t.Fatal(err)
	}
	truth = m.count(ctx)
	if m.load(t, h, dir) {
		t.Fatal("the owner's truth count must still catch an untouched write that changes the count")
	}
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}
