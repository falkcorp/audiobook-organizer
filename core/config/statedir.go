// file: core/config/statedir.go
// version: 1.0.0
// guid: 6f2db35a-2887-406b-8e92-baa5f1f3a3a3
// last-edited: 2026-11-10

package config

import (
	"errors"
	"path/filepath"
	"strings"
)

// errNoStateDirDefault is the default's error when an app declares no
// state-directory default and no explicit value is set.
func errNoStateDirDefault() error {
	return errors.New("config: no state directory: core.paths.state_dir is unset and the app declares no default")
}

// ResolveStateDir is the one pre-boot rule for core.paths.state_dir
// (05-config §1.7, C15, OPS3-1), HEAD's EnsureSecureStateDir
// (internal/config/state_dir.go) as a pure function of its inputs:
//
//  1. explicit (the env or flag value; blank is unset) is trimmed and
//     cleaned; when set it is created with mkdir and NEVER falls back: its
//     error is returned with the directory, so the caller names it;
//  2. otherwise def (app.Options.StateDir) is created with mkdir;
//  3. when def cannot be created, the directory of dbPath (core.storage.path
//     from the pre-boot layers, trimmed) is the fallback, only when it is
//     absolute and mkdir creates it; fellBack reports it, and the caller
//     warns. Otherwise def's own error is returned with def, never the
//     fallback's.
//
// The error is mkdir's own, unwrapped, as HEAD returns it: the kernel adds
// the canonical env name (<APP>_CORE_PATHS_STATE_DIR) and the remedies,
// which this pure function cannot know. ABO's state_dir.go delegates here, so
// the two cannot drift.
func ResolveStateDir(explicit, def, dbPath string, mkdir func(string) error) (dir string, fellBack bool, err error) {
	if v := strings.TrimSpace(explicit); v != "" {
		dir = filepath.Clean(v)
		if err := mkdir(dir); err != nil {
			return dir, false, err
		}
		return dir, false, nil
	}
	defErr := errNoStateDirDefault()
	if def != "" {
		if defErr = mkdir(def); defErr == nil {
			return def, false, nil
		}
	}
	fallback := filepath.Dir(strings.TrimSpace(dbPath))
	if fallback == "" || fallback == "." || !filepath.IsAbs(fallback) {
		return def, false, defErr
	}
	if ferr := mkdir(fallback); ferr != nil {
		return def, false, defErr // the real target's error, not the fallback's
	}
	return fallback, true, nil
}
