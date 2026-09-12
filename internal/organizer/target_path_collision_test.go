// file: internal/organizer/target_path_collision_test.go
// version: 1.0.0
// guid: 7c1a9e4f-2b3d-4c5e-8f6a-1d2e3f4a5b6c
// last-edited: 2026-09-11

package organizer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// collisionCounterValue reads the current total of
// audiobook_organizer_organize_target_path_collision_total straight from the
// default Prometheus gatherer. The counter var itself is unexported in
// package metrics, so a cross-package test cannot hold a Collector handle the
// way internal/metrics/metrics_test.go does — gathering by name is the
// equivalent read for an external caller. A family with no samples yet
// (before the first Inc) is treated as zero.
func collisionCounterValue(t *testing.T) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "audiobook_organizer_organize_target_path_collision_total" {
			continue
		}
		var sum float64
		for _, m := range mf.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
		return sum
	}
	return 0
}

// warnCapturingLogger wraps noopLogger to additionally record every Warn call
// (formatted, like the real loggers do) so the test can assert on the
// structured collision message without depending on a specific logger
// implementation's internals.
type warnCapturingLogger struct {
	noopLogger
	mu       sync.Mutex
	warnings []string
}

func (l *warnCapturingLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, fmt.Sprintf(msg, args...))
}

func (l *warnCapturingLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.warnings))
	copy(out, l.warnings)
	return out
}

// newCollisionMockStore builds a MockStore that can hydrate/update the given
// books by ID — the minimum organizeBooks' already-in-place branch needs
// (stampOrganizeMetadata -> hydrateAndUpdateBook -> GetBookByID/UpdateBook).
func newCollisionMockStore(books map[string]*database.Book) *database.MockStore {
	return &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := books[id]
			if !ok {
				return nil, nil
			}
			clone := *b
			return &clone, nil
		},
	}
}

// TestOrganizeBooks_LogsCollisionWhenTwoBooksShareATargetPath drives
// organizeBooks directly (bypassing FilterBooksNeedingOrganization, which
// would otherwise divert an already-at-target book into alreadyCorrect
// before it ever reaches the worker loop this brief instruments) with two
// different books whose Title/Author combination computes the identical
// target path, and a third, unrelated book whose target path does not
// collide with anything.
//
// Both colliding books already sit at that identical path (a real, if
// unusual, data state: two rows pointing at one file) so ReOrganizeInPlace's
// oldPath==targetPath no-op branch is what runs for each of them — the one
// branch where two different books can both succeed at the SAME final path
// without the existing _copyN dedup (nextAvailableTargetPath, organizer.go)
// or the exclusive-move hard-fail (moveExclusive) changing one of the paths
// or failing one of the books outright. That is exactly what DEC-11 asks to
// be counted: the collision as generateTargetPath computed it, not whatever
// a downstream mitigation reshaped it into.
func TestOrganizeBooks_LogsCollisionWhenTwoBooksShareATargetPath(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}
	t.Cleanup(func() { config.AppConfig = config.Config{} })

	metrics.Register()
	before := collisionCounterValue(t)

	org := NewOrganizer(&config.AppConfig)

	// Two books that share Title/Author -> generateTargetPath computes the
	// same path for both.
	sharedTemplate := &database.Book{
		Title:    "Shared Title",
		Author:   &database.Author{Name: "Shared Author"},
		FilePath: "placeholder.m4b", // only supplies the extension
	}
	sharedTarget, err := org.GenerateTargetPath(sharedTemplate)
	if err != nil {
		t.Fatalf("GenerateTargetPath(shared): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sharedTarget), 0o755); err != nil {
		t.Fatalf("mkdir shared target dir: %v", err)
	}
	if err := os.WriteFile(sharedTarget, []byte("shared bytes"), 0o644); err != nil {
		t.Fatalf("write shared target: %v", err)
	}

	// A third, unrelated book whose target path must NOT be reported as a
	// collision with anything — the anti-over-suppression case: a
	// known-good, non-colliding book must still organize cleanly with the
	// new detection active.
	otherTemplate := &database.Book{
		Title:    "Other Title",
		Author:   &database.Author{Name: "Other Author"},
		FilePath: "placeholder.m4b",
	}
	otherTarget, err := org.GenerateTargetPath(otherTemplate)
	if err != nil {
		t.Fatalf("GenerateTargetPath(other): %v", err)
	}
	if sharedTarget == otherTarget {
		t.Fatalf("test fixture bug: shared and other targets must differ, both are %q", sharedTarget)
	}
	if err := os.MkdirAll(filepath.Dir(otherTarget), 0o755); err != nil {
		t.Fatalf("mkdir other target dir: %v", err)
	}
	if err := os.WriteFile(otherTarget, []byte("other bytes"), 0o644); err != nil {
		t.Fatalf("write other target: %v", err)
	}

	bookA := &database.Book{ID: "book-a", Title: "Shared Title", Author: &database.Author{Name: "Shared Author"}, FilePath: sharedTarget}
	bookB := &database.Book{ID: "book-b", Title: "Shared Title", Author: &database.Author{Name: "Shared Author"}, FilePath: sharedTarget}
	bookC := &database.Book{ID: "book-c", Title: "Other Title", Author: &database.Author{Name: "Other Author"}, FilePath: otherTarget}

	mockDB := newCollisionMockStore(map[string]*database.Book{
		bookA.ID: bookA,
		bookB.ID: bookB,
		bookC.ID: bookC,
	})
	svc := NewService(mockDB)
	testLog := &warnCapturingLogger{}

	stats := svc.organizeBooks(context.Background(),
		[]database.Book{*bookA, *bookB, *bookC}, nil, testLog, "")

	// (a) No behavior change: all three books still organize successfully.
	if stats.Failed != 0 {
		t.Errorf("stats.Failed = %d, want 0 (detection must not fail a book)", stats.Failed)
	}
	if stats.Skipped != 0 {
		t.Errorf("stats.Skipped = %d, want 0 (detection must not skip a book)", stats.Skipped)
	}
	if stats.AlreadyCorrect != 3 {
		t.Errorf("stats.AlreadyCorrect = %d, want 3 (all three books were already at their computed target)", stats.AlreadyCorrect)
	}

	// (b) The collision counter increased by exactly 1 — one collision event
	// for the two books sharing sharedTarget, none for book-c.
	after := collisionCounterValue(t)
	if got := after - before; got != 1 {
		t.Fatalf("collision counter increased by %v, want exactly 1 (before=%v after=%v)", got, before, after)
	}

	// Exactly one collision warning was logged, naming both colliding book
	// IDs and the shared path — and it must not name book-c or otherTarget.
	warnings := testLog.snapshot()
	var collisionWarnings []string
	for _, w := range warnings {
		if !strings.Contains(w, "target path collision") {
			continue
		}
		collisionWarnings = append(collisionWarnings, w)
	}
	if len(collisionWarnings) != 1 {
		t.Fatalf("got %d collision warning(s), want exactly 1: %v", len(collisionWarnings), warnings)
	}
	msg := collisionWarnings[0]
	if !strings.Contains(msg, "book-a") || !strings.Contains(msg, "book-b") {
		t.Errorf("collision warning does not name both colliding books: %q", msg)
	}
	if !strings.Contains(msg, sharedTarget) {
		t.Errorf("collision warning does not name the shared path: %q", msg)
	}
	if strings.Contains(msg, "book-c") || strings.Contains(msg, otherTarget) {
		t.Errorf("collision warning incorrectly names the non-colliding book/path: %q", msg)
	}
}
