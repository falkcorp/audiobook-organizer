// file: core/backup/journal/hookrun_test.go
// version: 1.0.0
// guid: be4e1514-55f2-457c-b928-1ced9dfc10aa
// last-edited: 2026-11-12

package journal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

var hookRunAt = time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)

// TestHookRun_IDSurvivesUntilCleared: the ID of a restore hook run is the same
// for every call with the same binding (a re-run of the hooks after a crash)
// until ClearHookRun, and the next run gets another one. No run ID can equal a
// restore line's seq. The ID is in the state directory, mode 0600, and nowhere
// in the store.
func TestHookRun_IDSurvivesUntilCleared(t *testing.T) {
	dir := t.TempDir()

	first, err := EnsureHookRun(dir, "owed:7/ab", hookRunAt)
	if err != nil || !strings.HasPrefix(first, hookRunPrefix) || len(first) <= len(hookRunPrefix) {
		t.Fatalf("EnsureHookRun = %q, %v", first, err)
	}
	again, err := EnsureHookRun(dir, "owed:7/ab", hookRunAt.Add(time.Hour))
	if err != nil || again != first {
		t.Errorf("a second EnsureHookRun = %q, %v, want the first run's %q", again, err, first)
	}
	info, err := os.Stat(filepath.Join(dir, HookRunFile))
	if err != nil {
		t.Fatalf("the run ID is not in the state directory: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, want 0600", HookRunFile, info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, HookRunFile+".tmp")); !os.IsNotExist(err) {
		t.Errorf("the temporary file is still there: %v", err)
	}

	must(t, ClearHookRun(dir))
	if _, err := os.Stat(filepath.Join(dir, HookRunFile)); !os.IsNotExist(err) {
		t.Errorf("%s after ClearHookRun: %v, want it gone", HookRunFile, err)
	}
	must(t, ClearHookRun(dir)) // nothing recorded is not an error
	next, err := EnsureHookRun(dir, "owed:7/ab", hookRunAt.Add(2*time.Hour))
	if err != nil || next == first {
		t.Errorf("the run after ClearHookRun = %q, %v, want an ID other than %q", next, err, first)
	}
}

// TestHookRun_AFileMadeForAnotherBindingIsNeverReused: a file whose binding is not
// the run's belongs to a run that finished (a build that did not know the file
// finished it after a rollback) or to another store, and its ID must not become
// this run's key: a record keyed on it may be in the store.
func TestHookRun_AFileMadeForAnotherBindingIsNeverReused(t *testing.T) {
	dir := t.TempDir()
	first, err := EnsureHookRun(dir, "owed:7/ab", hookRunAt)
	must(t, err)

	other, err := EnsureHookRun(dir, "owed:9/cd", hookRunAt)
	if err != nil || other == first {
		t.Fatalf("EnsureHookRun for another binding = %q, %v, want an ID other than %q", other, err, first)
	}
	// The file now serves the new binding, and only it.
	if got, err := EnsureHookRun(dir, "owed:9/cd", hookRunAt); err != nil || got != other {
		t.Errorf("the new binding resumed as %q, %v, want %q", got, err, other)
	}
	if got, err := EnsureHookRun(dir, "owed:7/ab", hookRunAt); err != nil || got == first {
		t.Errorf("the old binding came back as %q, %v: its ID %q was reused", got, err, first)
	}
	if _, err := EnsureHookRun(dir, "", hookRunAt); err == nil {
		t.Error("EnsureHookRun with no binding succeeded, want an error")
	}
}

// TestHookRun_ADoubtfulFileIsReplacedNotTrusted: a file that does not decode, or
// holds an ID that is no run ID, gets a new ID; it is never an error that stops the
// boot and never a key.
func TestHookRun_ADoubtfulFileIsReplacedNotTrusted(t *testing.T) {
	for name, content := range map[string]string{
		"not json":          "{not json",
		"empty":             "",
		"no ID":             `{"id":"","binding":"x"}`,
		"an ID of a seq":    `{"id":"11","binding":"x"}`,
		"a restore's ID":    `{"id":"restore:r-dent:seq:11","binding":"x"}`,
		"a store row's ID":  `{"id":"2026-11-21T00:00:00Z-ab12"}`,
		"the JSON of null":  `null`,
		"an array":          `[]`,
		"binding of nobody": `{"id":"run:00112233445566778899aabbccddeeff","binding":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			must(t, os.WriteFile(filepath.Join(dir, HookRunFile), []byte(content), 0o600))
			id, err := EnsureHookRun(dir, "noline:3/n", hookRunAt)
			if err != nil || !strings.HasPrefix(id, hookRunPrefix) || id == "run:00112233445566778899aabbccddeeff" {
				t.Fatalf("EnsureHookRun over %q = %q, %v, want a freshly minted run ID", content, id, err)
			}
			if again, err := EnsureHookRun(dir, "noline:3/n", hookRunAt); err != nil || again != id {
				t.Errorf("the replaced file does not serve its own binding: %q, %v, want %q", again, err, id)
			}
		})
	}
}

// TestHookRun_TwoRunsNeverShareAnID: IDs come from crypto/rand, not from a clock or
// a counter a restored store could repeat.
func TestHookRun_TwoRunsNeverShareAnID(t *testing.T) {
	seen := map[string]bool{}
	for range 32 {
		dir := t.TempDir()
		id, err := EnsureHookRun(dir, "owed:7/ab", hookRunAt) // the same time and binding every run
		must(t, err)
		if seen[id] {
			t.Fatalf("run ID %q was minted twice", id)
		}
		seen[id] = true
	}
}

// TestClearLegacyHookRun: a c/backup/restore_run row (an older build's run ID, which
// an archive can carry) is deleted, and a store without one is left alone.
func TestClearLegacyHookRun(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	must(t, ClearLegacyHookRun(ctx, fx.ks)) // none is not an error

	b := fx.ks.NewBatch()
	must(t, b.In(fx.ks).Set([]byte(legacyHookRunKey), []byte(`{"id":"run:0123456789abcdef","at":"2026-11-12T01:00:00Z"}`)))
	must(t, b.Commit(ctx, storage.Sync))
	must(t, b.Close())

	must(t, ClearLegacyHookRun(ctx, fx.ks))
	if raw, err := fx.ks.Get(ctx, []byte(legacyHookRunKey)); err == nil {
		t.Errorf("the legacy row is still there: %q", raw)
	}
}
