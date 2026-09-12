// file: internal/scanner/organize_tally.go
// version: 1.2.0
// guid: 3e7b9c14-5a2d-4f86-b1c0-9d4e8a6f2b71
// last-edited: 2026-09-12

package scanner

import (
	"context"
	"maps"
	"sync"
)

// OrganizeTally accumulates the post-scan auto-organize outcome counts
// (adopted, suffixed, fragment_collapse, ...) across every folder of one scan
// run, so they can be persisted on the scan op's result.
//
// It rides the context rather than AutoOrganizeFn's signature for the same
// reason FileFailures does: the hook is wired by the server package to avoid an
// import cycle, and the counts have to come back out of it without the scanner
// learning the organizer's types. Keys are the organizer's outcome category
// strings; the scanner treats them as opaque.
type OrganizeTally struct {
	mu     sync.Mutex
	counts map[string]int
}

type organizeTallyKey struct{}

func withOrganizeTally(ctx context.Context, t *OrganizeTally) context.Context {
	return context.WithValue(ctx, organizeTallyKey{}, t)
}

func organizeTallyFrom(ctx context.Context) *OrganizeTally {
	t, _ := ctx.Value(organizeTallyKey{}).(*OrganizeTally)
	return t
}

// AddOrganizeTally folds counts into the scan run carried by ctx. It is a
// no-op outside a scan (a manual organize has no scan result to write to).
// Safe for concurrent use: folders can be organized by parallel hooks.
func AddOrganizeTally(ctx context.Context, counts map[string]int) {
	t := organizeTallyFrom(ctx)
	if t == nil || len(counts) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = make(map[string]int, len(counts))
	}
	for k, v := range counts {
		t.counts[k] += v
	}
}

// Snapshot returns a copy of the accumulated counts.
func (t *OrganizeTally) Snapshot() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.counts)
}
