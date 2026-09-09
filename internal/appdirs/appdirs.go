// file: internal/appdirs/appdirs.go
// version: 1.2.0
// guid: 3f8a1c07-92be-4d51-a6b3-0c47e5d19af2
// last-edited: 2026-09-09

// Package appdirs builds the set of application-owned directories that library
// sweeps must never descend into.
//
// It exists as its own package for one reason: pathutil is a leaf package and
// must not import internal/config or internal/backup, but SOMETHING has to do
// the resolving, and doing it inline at each walker would recreate the
// inventory-not-a-rule antipattern that pathutil's own doc comment warns
// about. Four walkers each hand-rolling "resolve backup_dir, then the dump
// dir, then playlists" is four chances to get it subtly different -- and the
// scanner's discovery walk and its file-count walk MUST agree exactly, or the
// progress denominator counts files that will never be scanned.
//
// So: one constructor, called by every walker, is the single source of truth.
package appdirs

import (
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// FromConfig resolves the application-owned directories from a config.
//
// Resolution rules, all of which matter for correctness downstream:
//
//   - backup_dir goes through backup.ResolveDir(configured, dbPath), which is
//     the single authority for that setting (a relative value is anchored to
//     the database's own directory). It is NOT re-implemented here; a second
//     copy of the rule would drift from the first.
//   - Every value is passed through cleanAbs, which drops anything empty or
//     non-absolute. An empty setting must not become a live path prefix, and a
//     relative one cannot be compared against the absolute paths a walk
//     yields. pathutil re-checks both -- this is defence in depth, not
//     duplication, because AppDirs can also be built by hand in tests.
//   - A directory configured outside the library root is kept as-is. It simply
//     never matches anything a library walk reaches, which needs no special
//     case and is not an error.
//
// A nil config yields a zero AppDirs: no exclusions beyond pathutil's dot
// rule, which is the pre-2026-08-29 behaviour and is fail-open by design.
func FromConfig(cfg *config.Config) pathutil.AppDirs {
	if cfg == nil {
		return pathutil.AppDirs{}
	}
	return pathutil.AppDirs{
		BackupDir:          cleanAbs(backup.ResolveDir(cfg.BackupDir, cfg.DatabasePath)),
		OpenLibraryDumpDir: cleanAbs(cfg.OpenLibraryDumpDir),
		PlaylistDir:        cleanAbs(cfg.PlaylistDir),
		// filepath.Dir of the database FILE, not the path itself. Both stores
		// are addressed by file: Pebble by its directory-shaped path, SQLite by
		// a real file. Taking Dir of each is what excludes the siblings that
		// resolve beside them -- certs/, library.bleve, .bootstrap-token, the
		// SQLite -wal and -shm.
		//
		// cleanAbs drops a relative value, which is the common default
		// ("audiobooks.pebble"). That is correct and deliberate: a relative
		// database path cannot be compared against the absolute paths a walk
		// yields, and guessing a base would exclude a subtree nobody
		// configured. See cleanAbs.
		DatabaseDir: cleanAbs(filepath.Dir(cfg.DatabasePath)),
		// ResolveActivityDBPath, not the raw field: the setting is empty by
		// default and resolves through three branches. Reading the field
		// directly would leave the DEFAULT location -- the one actually in use
		// almost everywhere -- unprotected, which is the failure this whole
		// change is about.
		ActivityDBDir: cleanAbs(filepath.Dir(cfg.ResolveActivityDBPath())),
	}
}

// Current builds AppDirs from the process-wide config. Walkers that already
// read config.AppConfig directly use this rather than threading a pointer.
func Current() pathutil.AppDirs {
	return FromConfig(&config.AppConfig)
}

// ClearedBaseline is what FromConfig still produces after a caller has zeroed
// every operator-settable field it reads. It exists for the `empty AppDirs`
// negative controls in the app_dir_guard tests, which assert that a walk with
// no exclusions configured sees the whole tree.
//
// Those controls used to compare against the zero AppDirs. That stopped being
// reachable when ActivityDBDir was added: ResolveActivityDBPath falls back to
// {RootDir}/.activity, and RootDir cannot be cleared because it IS the walk
// root. So exactly one field survives a full clear, and pretending otherwise
// would make every one of those tests fail for a reason that is not a bug.
//
// This is deliberately NOT `return FromConfig(cleared)` for some notion of a
// cleared config: ActivityDBDir is defined here as circular (whatever the live
// config resolves it to) precisely so that EVERY OTHER FIELD stays pinned at
// empty. That is what the controls are guarding. If FromConfig grows a new
// field tomorrow and a caller forgets to zero its source, `got` gains a value
// this baseline does not have and the control fails loudly -- which is the
// entire point of the assertion. Do not "simplify" this to ignore unknown
// fields.
func ClearedBaseline() pathutil.AppDirs {
	return pathutil.AppDirs{ActivityDBDir: Current().ActivityDBDir}
}

// cleanAbs normalises an absolute path and drops everything else.
//
// Dropping rather than guessing is deliberate: filepath.Abs would resolve a
// relative value against the process working directory, which could fabricate
// a match against a subtree nobody configured. Failing open (walking a
// directory we were unsure about) costs I/O; failing closed (skipping the
// library) is silent data loss.
func cleanAbs(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}
