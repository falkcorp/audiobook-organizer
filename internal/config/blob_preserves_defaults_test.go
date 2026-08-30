// file: internal/config/blob_preserves_defaults_test.go
// version: 1.1.0
// guid: 9e14c7b2-58fa-4d63-8071-2ab6f5cd913e
// last-edited: 2026-08-30

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
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// restoreAppConfig pins the process-wide config for the duration of one test.
//
// Every test in this file drives LoadConfigFromDatabase, which assigns the whole
// Config struct, so each one leaves the global in whatever state its blob
// produced. That made the package order-dependent: TestDefaultInheritanceIsLogged
// inherits however many non-zero keys the PREVIOUS test happened to leave behind,
// which under -shuffle ranged from ~2 (after a test that zeroed the struct) to
// 122 (after one that called ResetToDefaults). Measured on 2026-08-30: 20 of 24
// shuffle seeds failed.
func restoreAppConfig(t *testing.T) {
	t.Helper()
	before := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = before }) })
}

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
	restoreAppConfig(t)

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
	restoreAppConfig(t)

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
	restoreAppConfig(t)

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

// TestDefaultInheritanceIsLogged pins the audit. The seeding fix changes
// behaviour on every existing install, so the inheritance has to be visible at
// boot rather than discovered later from symptoms.
//
// The baseline is deliberately ResetToDefaults(), i.e. a FULL config, because
// that is the production shape: 122 keys inherit from a one-key blob. The
// original version of this test asserted that the rendered log line contained
// "scheduled.library_scan.interval", which is an assertion on presentation — the
// line caps its enumeration at maxAuditedKeysLogged (40) and that key sorts well
// past the cut. The assertion therefore passed only when some earlier test had
// left the global config mostly zeroed, and failed on 20 of 24 shuffle seeds.
//
// So the key-level assertion now runs against defaultsPreservedOverBlob, the
// untruncated set, which is what "the audit knows this key inherited" actually
// means and is immune to the config growing. The log assertions stay, because an
// accessor-only test would go green if someone deleted the slog.Info call.
func TestDefaultInheritanceIsLogged(t *testing.T) {
	restoreAppConfig(t)
	ResetToDefaults()

	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const blob = `{"root_dir":"/mnt/books"}`
	store := storeWithBlob(t, blob)
	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}

	// Behaviour: the audit's own key set must name the key whose silent
	// zeroing stopped production scanning for new books.
	inherited := defaultsPreservedOverBlob(blob, Snapshot())
	if !slices.Contains(inherited, "scheduled.library_scan.interval") {
		t.Errorf("the audit must name scheduled.library_scan.interval as inherited; got %d keys: %v",
			len(inherited), inherited)
	}
	if len(inherited) < 50 {
		t.Errorf("precondition: a full config over a one-key blob should inherit most of the "+
			"struct, so this test exercises the truncation boundary; got only %d keys", len(inherited))
	}

	// Presentation: the line is emitted, and it reconciles against itself —
	// count must be the whole set, not the length of the list it printed.
	//
	// This is asserted from the line's OWN attributes rather than against
	// `inherited` above, because the audit runs partway through
	// LoadConfigFromDatabase and later steps (secret rows, the maintenance
	// window migration) mutate the config before Snapshot() can see it. Two
	// configs would be compared and the counts would differ by whatever those
	// steps set.
	out := buf.String()
	if !strings.Contains(out, "kept their shipped default") {
		t.Fatalf("inheriting a default is a behaviour change and must be logged; got:\n%s", out)
	}
	count, keys, omitted := parseAuditLine(t, out)
	if count != len(keys)+omitted {
		t.Errorf("the audit line does not reconcile: count=%d but it printed %d names and "+
			"admitted to omitting %d. An operator cannot tell what is missing; got:\n%s",
			count, len(keys), omitted, out)
	}
	if count <= len(keys) {
		t.Errorf("precondition: this test exists to cover the truncating case; count=%d, printed=%d",
			count, len(keys))
	}
}

// parseAuditLine pulls count, the printed key names, and omitted back out of the
// rendered audit line so a test can check the line against itself.
func parseAuditLine(t *testing.T, out string) (count int, keys []string, omitted int) {
	t.Helper()
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "kept their shipped default") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no audit line in:\n%s", out)
	}
	m := regexp.MustCompile(`count=(\d+) keys="([^"]*)"`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("audit line has no count/keys attributes: %s", line)
	}
	count, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("count is not a number in: %s", line)
	}
	for _, k := range strings.Split(m[2], ", ") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if om := regexp.MustCompile(`omitted=(\d+)`).FindStringSubmatch(line); om != nil {
		if omitted, err = strconv.Atoi(om[1]); err != nil {
			t.Fatalf("omitted is not a number in: %s", line)
		}
	}
	return count, keys, omitted
}

// TestTruncatedAuditLineReportsHowManyKeysItHid is the other half of the
// truncation story. "(list truncated)" told an operator that names were missing
// without telling them how many, so the line could not be reconciled against the
// count by eye. It must now say so as a number.
func TestTruncatedAuditLineReportsHowManyKeysItHid(t *testing.T) {
	restoreAppConfig(t)
	ResetToDefaults()

	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const blob = `{"root_dir":"/mnt/books"}`
	inherited := defaultsPreservedOverBlob(blob, Snapshot())
	if len(inherited) <= maxAuditedKeysLogged {
		t.Fatalf("precondition: need more than %d inherited keys to truncate; got %d",
			maxAuditedKeysLogged, len(inherited))
	}

	logDefaultsPreservedOverBlob(blob, Snapshot())

	out := buf.String()
	if want := fmt.Sprintf("omitted=%d", len(inherited)-maxAuditedKeysLogged); !strings.Contains(out, want) {
		t.Errorf("a truncated audit line must name the shortfall as %q; got:\n%s", want, out)
	}
	// The renderer is driven directly here, so its input is exactly the config
	// the accessor was given: the reported count must equal the whole set.
	if want := fmt.Sprintf("count=%d", len(inherited)); !strings.Contains(out, want) {
		t.Errorf("a truncated line must still report the EXACT total %q, not the printed "+
			"length; got:\n%s", want, out)
	}
}

// TestUntruncatedAuditLineClaimsNothingWasHidden is the discriminating half: a
// line that fits must not carry an omitted= attribute, or the attribute stops
// meaning anything.
func TestUntruncatedAuditLineClaimsNothingWasHidden(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { *c = Config{RootDir: "/mnt/books", LogLevel: "info", CacheSize: 1000} })

	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const blob = `{"root_dir":"/mnt/books"}`
	inherited := defaultsPreservedOverBlob(blob, Snapshot())
	if len(inherited) == 0 || len(inherited) > maxAuditedKeysLogged {
		t.Fatalf("precondition: need 1..%d inherited keys; got %d", maxAuditedKeysLogged, len(inherited))
	}

	logDefaultsPreservedOverBlob(blob, Snapshot())

	out := buf.String()
	if !strings.Contains(out, "kept their shipped default") {
		t.Fatalf("the audit line must still be emitted; got:\n%s", out)
	}
	if strings.Contains(out, "omitted=") || strings.Contains(out, "truncated") {
		t.Errorf("nothing was cut, so the line must not claim a shortfall; got:\n%s", out)
	}
	// Every name fits, so every name must actually be there.
	for _, k := range inherited {
		if !strings.Contains(out, k) {
			t.Errorf("inherited key %q is missing from an untruncated line; got:\n%s", k, out)
		}
	}
}

// TestNoAuditLineWhenBlobIsComplete is the discriminating half: a blob that
// already specifies everything must not produce a spurious audit line, or the
// message becomes noise everyone learns to ignore.
func TestNoAuditLineWhenBlobIsComplete(t *testing.T) {
	restoreAppConfig(t)

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
