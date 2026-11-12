// file: core/kernel/sectioncache_test.go
// version: 1.0.2
// guid: 9e049097-4cad-41aa-a27d-ddd611ce22d0
// last-edited: 2026-11-12

package kernel

import (
	"context"
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
	plain, err := newCoreConfig("gotham", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := newCoreConfig("gotham", cache)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newCoreConfig("arkham", cache)
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
	boot := func(ctx context.Context) {
		t.Helper()
		c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()))
		k, err := New(ctx, c)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() {
			if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
				t.Errorf("close: %v", err)
			}
		})
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
