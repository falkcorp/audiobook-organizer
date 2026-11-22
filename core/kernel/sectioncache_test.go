// file: core/kernel/sectioncache_test.go
// version: 1.0.2
// guid: 9e049097-4cad-41aa-a27d-ddd611ce22d0
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/tasks"
)

// TestBootSectionCache: a boot gets the section cache a test put in its
// Seams, none when it carries none, and an error, not a silent miss, when the
// seam holds something else.
func TestBootSectionCache(t *testing.T) {
	if c, err := bootSectionCache(t.Context()); c != nil || err != nil {
		t.Errorf("no Seams: (%v, %v), want (nil, nil)", c, err)
	}
	if c, err := bootSectionCache(bootseam.With(t.Context(), &bootseam.Seams{})); c != nil || err != nil {
		t.Errorf("empty Seams: (%v, %v), want (nil, nil)", c, err)
	}
	want := config.NewSectionCache()
	if got, err := bootSectionCache(bootseam.With(t.Context(), &bootseam.Seams{SectionCache: want})); got != want || err != nil {
		t.Errorf("Seams.SectionCache: (%v, %v), want the one given", got, err)
	}
	if _, err := bootSectionCache(bootseam.With(t.Context(), &bootseam.Seams{SectionCache: "bruce"})); err == nil {
		t.Error("a Seams.SectionCache of the wrong type did not fail the boot")
	}
}

// TestNewCoreConfig_SharedCache: a core config over a shared cache is the one
// over none: the same leaves with the same defaults, and a write through one
// store never shows in another over the same cache.
func TestNewCoreConfig_SharedCache(t *testing.T) {
	cache := config.NewSectionCache()
	plain, err := newCoreConfig("gotham", CoreDefaults{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := newCoreConfig("gotham", CoreDefaults{}, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newCoreConfig("arkham", CoreDefaults{}, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, c := range []*coreConfig{plain, first, second} {
			_ = c.close(t.Context())
		}
	})
	keys := func(c *coreConfig) []string {
		var out []string
		for k := range c.leaves {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	if !slices.Equal(keys(plain), keys(first)) || !slices.Equal(keys(plain), keys(second)) {
		t.Error("the leaves of a core config over a shared cache differ from one over none")
	}
	if first.tasks == second.tasks || first.store == second.store {
		t.Error("two core configs over one cache share a store or a handle")
	}
}

// TestKernelBoot_CoreConfigUsesSeamCache: a boot over a context carrying a
// section cache builds the core config sections through it when the tasks
// wiring reads the core config (a registered definition: the config is built
// lazily at P7), so the cache holds them afterwards, and a second boot over it adds none:
// the wiring the crash matrix relies on to build the core sections once per run, not once per
// boot. A boot with no cache leaves it alone.
func TestKernelBoot_CoreConfigUsesSeamCache(t *testing.T) {
	cache := config.NewSectionCache()
	ctx := bootseam.With(t.Context(), &bootseam.Seams{SectionCache: cache})
	// core.paths defaults to the state directory, so a distinct directory per
	// boot is a distinct template; the boots share one, emptied between them
	// (each has a store of its own, and a keyring names its store).
	state := t.TempDir()
	boot := func(ctx context.Context) {
		t.Helper()
		c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()))
		c.StateDirDefault = state
		k, err := newKCtx(t, ctx, c)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
			t.Errorf("close: %v", err)
		}
		ents, err := os.ReadDir(state)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if err := os.RemoveAll(filepath.Join(state, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	boot(t.Context())
	if got := cache.Len(); got != 0 {
		t.Fatalf("a boot with no cache in its context filled one: %d templates", got)
	}
	boot(ctx)
	first := cache.Len()
	if first == 0 {
		t.Fatal("the boot built no core section through the cache in its context")
	}
	boot(ctx)
	if got := cache.Len(); got != first {
		t.Errorf("a second boot over the same cache holds %d templates, want %d: it built the core sections again", got, first)
	}
}

// TestKernelBoot_EveryConfigStoreUsesSeamCache: every config store a boot over
// a section cache makes is built over that cache, step 0's scratch judgement
// store included, and a second boot over it builds no template the first did
// not. The Len check of TestKernelBoot_CoreConfigUsesSeamCache cannot see a
// store that bypasses the cache (it builds uncached and leaves Len alone);
// step 0's CheckConfig store did exactly that once, because it spelled its
// options out by hand and lost SectionCache, and the hello crash matrix ran
// three times slower until it timed out in CI.
//
// Step 0 builds its judgement store only when a retired name is set
// (refuseRetiredAtStep0), so the boots here set one, a warn-class env name;
// the last boot sets none and must make exactly one store fewer: the skip
// is the judgement store and nothing else.
func TestKernelBoot_EveryConfigStoreUsesSeamCache(t *testing.T) {
	cache := config.NewSectionCache()
	retired := []config.RetiredName{{Name: "HELLO_ALFRED", Message: "the butler retired"}}
	environ := []string{"HELLO_ALFRED=pennyworth"}
	var stores, bypassed int
	seen := func(sc any) {
		stores++
		if c, _ := sc.(*config.SectionCache); c != cache {
			bypassed++
		}
	}

	ctx := bootseam.With(t.Context(), &bootseam.Seams{SectionCache: cache, ConfigStore: seen})
	state := t.TempDir()
	boot := func(environ []string) {
		t.Helper()
		c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()))
		c.StateDirDefault = state
		c.ConfigOptions.Retired = retired
		c.ConfigOptions.Environ = append(slices.Clone(c.ConfigOptions.Environ), environ...)
		k, err := newKCtx(t, ctx, c)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
			t.Errorf("close: %v", err)
		}
		ents, err := os.ReadDir(state)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if err := os.RemoveAll(filepath.Join(state, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	boot(environ)
	if stores < 2 {
		t.Fatalf("the boot made %d config stores, want step 0's store and its judgement store at least", stores)
	}
	if bypassed != 0 {
		t.Errorf("%d of the boot's %d config stores were built without the section cache in its context", bypassed, stores)
	}
	first, judged := cache.Len(), stores
	stores = 0
	boot(environ)
	if got := cache.Len(); got != first {
		t.Errorf("a second boot over the same cache holds %d templates, want %d", got, first)
	}
	stores = 0
	boot(nil)
	if stores != judged-1 {
		t.Errorf("a boot with no retired name set made %d config stores, one with one set %d: want exactly the judgement store fewer", stores, judged)
	}
}
