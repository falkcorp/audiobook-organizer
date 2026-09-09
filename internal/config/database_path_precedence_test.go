// file: internal/config/database_path_precedence_test.go
// version: 1.0.0
// guid: 5b2e9c14-7a38-4d6f-9e21-c0a4b7d3e582
// last-edited: 2026-09-09

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// bindDBFlag reproduces cmd/root.go's --db wiring faithfully: a pflag WITH a
// registered default, bound into viper.
//
// Faithfully matters here. The bug this file guards is caused by that default,
// and a test that used viper.Set instead of a real bound flag would exercise
// viper's override layer, which behaves differently and would pass whether or
// not the fix is correct.
func bindDBFlag(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("db", "audiobooks.pebble", "path to database")
	if err := viper.BindPFlag("database_path", fs.Lookup("db")); err != nil {
		t.Fatalf("BindPFlag: %v", err)
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}
	return fs
}

func resetFlags(t *testing.T) {
	t.Helper()
	ResetExplicitFlagsForTest()
	t.Cleanup(ResetExplicitFlagsForTest)
}

// TestDatabasePath_ExplicitFlagBeatsTheBlob is the regression guard for the
// 2026-09-09 production outage.
//
// The app had just been moved to a new database location. Its systemd unit
// passed --db (and DATABASE_PATH) pointing at the new path, and it opened that
// database correctly — cmd/root.go's serve command calls initializeStore with
// AppConfig.DatabasePath BEFORE the blob is loaded. Then
// LoadConfigFromDatabase restored the config blob written months earlier, which
// still said /var/lib/audiobook-organizer/audiobooks.pebble, and nothing
// re-applied the operator's value on top. Twenty lines later Validate() rejected
// the restored path because that directory no longer existed, and serve returned
// an error.
//
// So the process killed itself over a stale *string describing* the database it
// was already successfully running against. The worse failure is the one that
// did not happen: had the stale path still existed, the app would have quietly
// reported the wrong location — or, on a later code path, opened the wrong
// database — while --db on the command line said otherwise.
func TestDatabasePath_ExplicitFlagBeatsTheBlob(t *testing.T) {
	resetViper(t)
	resetFlags(t)
	bindDBFlag(t, "--db", "/mnt/bigdata/books/audiobook-organizer/.appdata/audiobooks.pebble")
	MarkFlagExplicit("database_path")

	c := &Config{DatabasePath: "/var/lib/audiobook-organizer/audiobooks.pebble"} // from the blob
	applyEnvAuthoritativeConfig(c)

	const want = "/mnt/bigdata/books/audiobook-organizer/.appdata/audiobooks.pebble"
	if c.DatabasePath != want {
		t.Fatalf("DatabasePath = %q, want %q — an explicitly typed --db must beat the "+
			"persisted blob, or the operator's command line is silently ignored", c.DatabasePath, want)
	}
}

// TestDatabasePath_UnsuppliedFlagLeavesTheBlobAlone is the negative control, and
// it is the test that gives the one above any meaning.
//
// The flag is BOUND but never typed, exactly as on a bare `serve`. viper still
// answers "audiobooks.pebble" for database_path, because a bound flag's default
// is a value like any other. An unguarded assignment would therefore pass the
// test above AND clobber every deployment's persisted path with a relative
// "audiobooks.pebble" — turning a fix for one host into data loss for the rest.
func TestDatabasePath_UnsuppliedFlagLeavesTheBlobAlone(t *testing.T) {
	resetViper(t)
	resetFlags(t)
	fs := bindDBFlag(t) // bound, not typed

	if fs.Changed("db") {
		t.Fatal("precondition broken: pflag reports --db as Changed without it being passed")
	}
	// IsSet and Get DISAGREE here, and the disagreement is the point: IsSet skips
	// the "fall back to the flag's default" branch that Get takes. A guard built
	// on the resolved VALUE is therefore wrong even though IsSet reads false.
	// See TestViperIsSetForABoundFlagIsNotAValueGuard for the other row of the
	// table — one SetDefault flips this to IsSet=true, Get="".
	if viper.IsSet("database_path") {
		t.Fatal("precondition changed: viper.IsSet is now TRUE for a bound, untyped flag — " +
			"re-measure the table on config.explicitFlags before trusting either comment")
	}
	if got := viper.GetString("database_path"); got != "audiobooks.pebble" {
		t.Fatalf("precondition broken: expected the flag default, got %q", got)
	}

	const want = "/var/lib/audiobook-organizer/audiobooks.pebble"
	c := &Config{DatabasePath: want}
	applyEnvAuthoritativeConfig(c)

	if c.DatabasePath != want {
		t.Fatalf("DatabasePath = %q, want the blob value %q — an untyped --db carries a "+
			"registered default, and assigning it unconditionally would repoint every "+
			"deployment at a relative path", c.DatabasePath, want)
	}
}

// TestViperIsSetForABoundFlagIsNotAValueGuard pins the measured table quoted in
// the comment on config.explicitFlags, so the justification for flagSupplied
// cannot rot into a story nobody re-checked.
//
//	BindPFlag only:              IsSet=false  Get="audiobooks.pebble"
//	BindPFlag + SetDefault(""):  IsSet=true   Get=""
//
// Row 1 is why a value-based guard is wrong: IsSet says "nobody set this" while
// Get hands back a real-looking path. Row 2 is the trap for a future reader —
// adding one SetDefault line for database_path, mirroring the one InitConfig
// already registers for activity_db_path, flips IsSet permanently true (breaking
// an IsSet guard) and makes Get return the EMPTY default rather than the flag's,
// because SetDefault outranks a bound flag's default. flagSupplied is immune to
// both because it never consults viper at all.
func TestViperIsSetForABoundFlagIsNotAValueGuard(t *testing.T) {
	t.Run("bound flag only", func(t *testing.T) {
		resetViper(t)
		bindDBFlag(t)
		if viper.IsSet("database_path") {
			t.Error("IsSet=true for a bound, untyped flag with no SetDefault")
		}
		if got := viper.GetString("database_path"); got != "audiobooks.pebble" {
			t.Errorf("Get=%q, want the flag default — IsSet and Get must still disagree", got)
		}
	})

	t.Run("bound flag plus SetDefault", func(t *testing.T) {
		resetViper(t)
		bindDBFlag(t)
		viper.SetDefault("database_path", "")
		if !viper.IsSet("database_path") {
			t.Error("IsSet=false after SetDefault — the trap this test documents has changed")
		}
		if got := viper.GetString("database_path"); got != "" {
			t.Errorf("Get=%q, want \"\" — SetDefault is supposed to outrank the flag's "+
				"own default. If that changed, the warning against adding a SetDefault "+
				"for database_path needs re-deriving, not deleting", got)
		}
	})
}

// TestDatabasePath_EnvBeatsTheBlob covers the other operator lever. Production's
// drop-in sets both DATABASE_PATH and --db; either alone must be sufficient.
//
// Note what makes this work: cmd/root.go calls viper.AutomaticEnv() and no
// SetEnvPrefix, so the key database_path maps to DATABASE_PATH with no prefix.
// envSupplied reads os.LookupEnv and viper.GetString reads viper — they agree
// only because of that. If a prefix is ever introduced, this test fails rather
// than the guard passing while GetString quietly returns the flag default.
func TestDatabasePath_EnvBeatsTheBlob(t *testing.T) {
	resetViper(t)
	resetFlags(t)
	const want = "/mnt/bigdata/books/audiobook-organizer/.appdata/audiobooks.pebble"
	t.Setenv("DATABASE_PATH", want)
	viper.AutomaticEnv()
	bindDBFlag(t) // bound, not typed: the env must win on its own

	if got := viper.GetString("database_path"); got != want {
		t.Fatalf("viper resolved database_path to %q, want the environment value %q — "+
			"AutomaticEnv no longer maps this key, so the guard and the read disagree", got, want)
	}

	c := &Config{DatabasePath: "/var/lib/audiobook-organizer/audiobooks.pebble"}
	applyEnvAuthoritativeConfig(c)

	if c.DatabasePath != want {
		t.Fatalf("DatabasePath = %q, want %q", c.DatabasePath, want)
	}
}

// TestDatabasePath_EmptyEnvIsNotASupplier pins the same choice envSupplied makes
// everywhere else: systemd's `Environment=DATABASE_PATH=` must behave like an
// unset variable, not blank the persisted path.
func TestDatabasePath_EmptyEnvIsNotASupplier(t *testing.T) {
	resetViper(t)
	resetFlags(t)
	t.Setenv("DATABASE_PATH", "")
	viper.AutomaticEnv()
	bindDBFlag(t)

	const want = "/var/lib/audiobook-organizer/audiobooks.pebble"
	c := &Config{DatabasePath: want}
	applyEnvAuthoritativeConfig(c)

	if c.DatabasePath != want {
		t.Fatalf("DatabasePath = %q, want %q — an empty environment value must not "+
			"count as an override", c.DatabasePath, want)
	}
}

// TestRootDirIsDeliberatelyNotEnvAuthoritative pins an omission, because an
// omission is invisible and the next reader will "notice" that root_dir has the
// same shape as database_path and add it.
//
// It must not be added while the tracked unit ships
// AUDIOBOOK_ROOT_DIR=/var/lib/audiobooks, a directory that does not exist on the
// production host, where the real library path lives in the database. Making the
// environment authoritative for root_dir would point the library at nothing on
// the next boot. Fix the unit first, then this.
func TestRootDirIsDeliberatelyNotEnvAuthoritative(t *testing.T) {
	resetViper(t)
	resetFlags(t)
	t.Setenv("AUDIOBOOK_ROOT_DIR", "/var/lib/audiobooks")
	viper.AutomaticEnv()
	viper.Set("root_dir", "/var/lib/audiobooks")
	MarkFlagExplicit("root_dir")

	const want = "/mnt/bigdata/books"
	c := &Config{RootDir: want}
	applyEnvAuthoritativeConfig(c)

	if c.RootDir != want {
		t.Fatalf("RootDir = %q, want the blob value %q — root_dir is deliberately NOT "+
			"environment-authoritative; see the comment at the end of "+
			"applyEnvAuthoritativeConfig before changing this", c.RootDir, want)
	}
}

// TestSettingLocks_NameTheMechanism covers the UI annotation. A disabled field
// that blames the wrong mechanism is worse than one that says nothing: it sends
// the operator to delete an environment variable that was never the cause.
func TestSettingLocks_NameTheMechanism(t *testing.T) {
	resetViper(t)
	resetFlags(t)

	if got := SettingLockedBy("database_path"); got != "" {
		t.Fatalf("SettingLockedBy = %q with neither flag nor env supplied, want \"\"", got)
	}

	MarkFlagExplicit("database_path")
	if got := SettingLockedBy("database_path"); got != "--db" {
		t.Fatalf("SettingLockedBy = %q, want \"--db\"", got)
	}

	// Both at once: the key must be reported ONCE, and the environment named
	// first — it is the layer an operator is most likely to be able to change.
	t.Setenv("DATABASE_PATH", "/mnt/x/audiobooks.pebble")
	if got := SettingLockedBy("database_path"); got != "DATABASE_PATH" {
		t.Fatalf("SettingLockedBy = %q with both supplied, want \"DATABASE_PATH\"", got)
	}
	locked := EnvLockedSettings()
	var n int
	for _, k := range locked {
		if k == "database_path" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("database_path appears %d times in EnvLockedSettings %v, want exactly 1 — "+
			"the UI renders this list directly", n, locked)
	}
	if got := SettingLocks()["database_path"]; got != "DATABASE_PATH" {
		t.Fatalf("SettingLocks()[database_path] = %q, want \"DATABASE_PATH\"", got)
	}
}

// TestValidateParentDirExists_ReportsTheRealError guards the message that made
// the outage take longer than it should have.
//
// Every stat failure used to be reported as "does not exist". The first guess at
// the console was a permissions problem, and the message gave no way to confirm
// or rule it out — the sentence was the same either way. A non-directory parent
// is the cheapest errno to provoke portably (ENOTDIR); EACCES needs a non-root
// runner and would skip in CI.
func TestValidateParentDirExists_ReportsTheRealError(t *testing.T) {
	dir := t.TempDir()

	notADir := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := validateParentDirExists(filepath.Join(notADir, "child", "audiobooks.pebble"), "database_path")
	if err == nil {
		t.Fatal("expected an error when the parent is not a directory")
	}
	if strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error says %q — the parent is a FILE, not missing. Reporting every "+
			"stat failure as \"does not exist\" is what sent the 2026-09-09 outage "+
			"looking for a missing directory that was sitting right there.", err)
	}

	// The genuinely-missing case must still say so, or the fix has just traded
	// one wrong message for another.
	err = validateParentDirExists(filepath.Join(dir, "nope", "audiobooks.pebble"), "database_path")
	if err == nil {
		t.Fatal("expected an error when the parent is missing")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error says %q, want it to report a missing directory", err)
	}

	// And a real directory must still pass.
	if err := validateParentDirExists(filepath.Join(dir, "audiobooks.pebble"), "database_path"); err != nil {
		t.Fatalf("a valid parent directory was rejected: %v", err)
	}
}
