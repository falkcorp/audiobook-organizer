// file: core/module/resetplan_test.go
// version: 1.0.0
// guid: 2d8e5a71-4c3b-4f69-b0e2-9a6f1c7d3e58
// last-edited: 2026-11-08

package module

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// TestResetPlanContext: the reset hook's Plan reads its storage and the
// kernel's rule (4) from the context the kernel builds, and neither from any
// other context; a nil rule is no rule.
func TestResetPlanContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := ResetPlanStorage(ctx); ok {
		t.Fatal("ResetPlanStorage in a plain context")
	}
	if _, ok := ResetPlanKept(ctx); ok {
		t.Fatal("ResetPlanKept in a plain context")
	}
	if _, ok := ResetPlanKept(WithResetPlanKept(ctx, nil)); ok {
		t.Fatal("ResetPlanKept with a nil rule")
	}
	h := StorageHandles{Legacy: storage.LegacyRange{}}
	kept := func(p string) bool { return p == "/batcave" }
	pctx := WithResetPlanStorage(WithResetPlanKept(ctx, kept), h)
	if got, ok := ResetPlanStorage(pctx); !ok || got.Legacy != h.Legacy {
		t.Fatalf("ResetPlanStorage = %+v, %v", got, ok)
	}
	got, ok := ResetPlanKept(pctx)
	if !ok || !got("/batcave") || got("/arkham") {
		t.Fatal("ResetPlanKept is not the rule the kernel set")
	}
}
