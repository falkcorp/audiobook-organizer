// file: internal/config/blob_preserves_defaults_test.go
// version: 1.0.0
// guid: 9e14c7b2-58fa-4d63-8071-2ab6f5cd913e
// last-edited: 2026-08-12

// Regression tests for the stored config blob wiping every default.
//
// LoadConfigFromDatabase used to unmarshal config_blob into a fresh, all-zero
// `var loaded Config` and then assign `*c = loaded`. encoding/json only writes
// the fields the JSON actually contains, so any field ABSENT from the blob kept
// the zero value of that fresh struct — and the whole-struct assignment then
// threw away the viper defaults that had just been loaded.
//
// A blob is written once and re-read on every boot, so this pinned every field
// added to Config after that blob was saved to zero, permanently. No later
// change to a default could reach the install.
//
// Measured on production 2026-08-12: every key under scheduled.* was
// {enabled:false, interval:0}, including library_scan, whose shipped defaults
// are enabled=true / interval=360. library_scan is the only unattended
// discovery path for newly added books, so nothing had been scanning
// automatically. The feature shipped (#2315) and deployed correctly; the blob
// zeroed it on load.
//
// The fix seeds `loaded` from Snapshot() so absent means "keep the default"
// rather than "use zero". A value the operator genuinely set to false/0 is
// PRESENT in the blob and must still win — that is asserted here too, because a
// fix that made stored zeros stop working would be a worse bug than the one it
// replaced.

package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// storeWithBlob builds a settings store holding exactly one config_blob.
func storeWithBlob(t *testing.T, blob string) *mockSettingsStore {
	t.Helper()
	if !json.Valid([]byte(blob)) {
		t.Fatalf("test blob is not valid JSON: %s", blob)
	}
	store := newMockSettingsStore()
	store.settings["config_blob"] = &database.Setting{
		Key:   "config_blob",
		Value: blob,
		Type:  "json",
	}
	return store
}

// TestBlobWithoutScheduledKeysKeepsDefaults is the core regression. A blob
// written before scheduled.* existed must not zero it.
func TestBlobWithoutScheduledKeysKeepsDefaults(t *testing.T) {
	// Establish the pre-blob state: what viper/InitConfig would have left in
	// AppConfig before the stored blob is applied on top.
	Mutate(func(c *Config) {
		c.Scheduled.LibraryScan.Enabled = true
		c.Scheduled.LibraryScan.Interval = 360
	})

	// A blob from an older version: it knows about root_dir, and nothing at all
	// about the scheduled block.
	store := storeWithBlob(t, `{"root_dir":"/mnt/books"}`)

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}

	got := Snapshot()

	if got.RootDir != "/mnt/books" {
		t.Errorf("the blob must still apply the fields it DOES contain; root_dir = %q", got.RootDir)
	}

	// The defect: these came back false/0 and the periodic scan never ran.
	if !got.Scheduled.LibraryScan.Enabled {
		t.Error("library_scan.enabled was zeroed by a blob that never mentioned it — " +
			"this is the defect that stopped anything from scanning for new books")
	}
	if got.Scheduled.LibraryScan.Interval != 360 {
		t.Errorf("library_scan.interval was zeroed by a blob that never mentioned it; "+
			"want the default 360, got %d. Interval 0 means the scheduler creates no "+
			"ticker at all", got.Scheduled.LibraryScan.Interval)
	}
}

// TestBlobExplicitFalseStillWins is the discriminating half: the fix must not
// make stored values unwritable. An operator who deliberately turned the scan
// off has that choice recorded IN the blob, and it must survive.
func TestBlobExplicitFalseStillWins(t *testing.T) {
	Mutate(func(c *Config) {
		c.Scheduled.LibraryScan.Enabled = true
		c.Scheduled.LibraryScan.Interval = 360
	})

	store := storeWithBlob(t, `{"scheduled":{"library_scan":{"enabled":false,"interval":0}}}`)

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}

	got := Snapshot()
	if got.Scheduled.LibraryScan.Enabled {
		t.Error("an explicit stored false must override the default — otherwise turning " +
			"a task off would be impossible")
	}
	if got.Scheduled.LibraryScan.Interval != 0 {
		t.Errorf("an explicit stored 0 must override the default; got %d",
			got.Scheduled.LibraryScan.Interval)
	}
}

// TestBlobPartialOverrideLeavesSiblingsAlone pins the per-field granularity:
// setting one key in a nested block must not reset the others in that block.
func TestBlobPartialOverrideLeavesSiblingsAlone(t *testing.T) {
	Mutate(func(c *Config) {
		c.Scheduled.LibraryScan.Enabled = true
		c.Scheduled.LibraryScan.Interval = 360
	})

	// Operator changed only the interval.
	store := storeWithBlob(t, `{"scheduled":{"library_scan":{"interval":120}}}`)

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}

	got := Snapshot()
	if got.Scheduled.LibraryScan.Interval != 120 {
		t.Errorf("stored interval should apply; got %d", got.Scheduled.LibraryScan.Interval)
	}
	if !got.Scheduled.LibraryScan.Enabled {
		t.Error("changing the interval must not silently disable the task — " +
			"a sibling field absent from the blob keeps its default")
	}
}

// TestDefaultInheritanceIsLogged pins the audit line. The seeding fix changes
// behaviour on every existing install — measured upper bound on production was
// 33 keys — so the inheritance has to be visible at boot rather than discovered
// later from symptoms.
func TestDefaultInheritanceIsLogged(t *testing.T) {
	Mutate(func(c *Config) {
		c.Scheduled.LibraryScan.Enabled = true
		c.Scheduled.LibraryScan.Interval = 360
	})

	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	store := storeWithBlob(t, `{"root_dir":"/mnt/books"}`)
	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "kept their shipped default") {
		t.Fatalf("inheriting a default is a behaviour change and must be logged; got:\n%s", out)
	}
	if !strings.Contains(out, "scheduled.library_scan.interval") {
		t.Errorf("the audit must name the inherited keys, not just count them; got:\n%s", out)
	}
}

// TestNoAuditLineWhenBlobIsComplete is the discriminating half: a blob that
// already specifies everything must not produce a spurious audit line, or the
// message becomes noise everyone learns to ignore.
func TestNoAuditLineWhenBlobIsComplete(t *testing.T) {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Seed a config that is entirely zero, so nothing non-zero can be inherited.
	Mutate(func(c *Config) { *c = Config{} })

	store := storeWithBlob(t, `{"root_dir":"/mnt/books"}`)
	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}

	if strings.Contains(buf.String(), "kept their shipped default") {
		t.Errorf("no non-zero default was inherited, so there must be no audit line; got:\n%s", buf.String())
	}
}
