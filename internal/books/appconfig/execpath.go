// file: internal/books/appconfig/execpath.go
// version: 1.0.0
// guid: 6f2d9a41-c8b3-4e07-9d15-a3e0b7c64f28
// last-edited: 2026-11-19

package appconfig

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// execPathRows are the settings that name a program the server runs or the
// directory it installs and runs managed tools from. HEAD let any interactive
// session write them (protected_fields.go: FieldExecutable) and checked nothing
// about the value, so a settings file or PUT that names /tmp/x as
// tools.fpcalc.custom_path made the next fingerprint run execute it as the service
// user. The write is still allowed (an admin who installs fpcalc somewhere sets
// it here), but the value must be an absolute path to something an unprivileged
// user cannot have swapped: a regular file that exists, writable by no one but its
// owner and group, in a directory the same holds for. A directory row (the managed
// install directory) may not exist yet, since the installer creates it.
var execPathRows = map[string]execPathRow{
	"tools.fpcalc.custom_path": {execFile, func(c config.Config) string { return c.Tools.Fpcalc.CustomPath }},
	"tools.ollama.custom_path": {execFile, func(c config.Config) string { return c.Tools.Ollama.CustomPath }},
	"tools.managed_dir":        {execDir, func(c config.Config) string { return c.Tools.ManagedDir }},
}

// execPathRow is a row's rule and where its current value is read from: a value a
// write leaves as it is (a Settings save sends every key back) is not judged again,
// so a stored path that has since gone does not make every save of the page fail.
type execPathRow struct {
	kind execPathKind
	cur  func(config.Config) string
}

// execModeRows are the sibling rows of execPathRows that choose between the managed
// binary and the custom path: not paths, so nothing to validate, and listed so a
// new tools.* row is classified on purpose (TestExecPaths_EveryToolsRowIsClassified).
var execModeRows = []string{"tools.fpcalc.mode", "tools.ollama.mode"}

type execPathKind int

const (
	execFile execPathKind = iota // must exist and be a regular file
	execDir                      // an absolute directory; may not exist yet
)

// execPathSets are the executable-path rows the update would have judged, each
// with the value it sets: set to a non-empty value the write changes.
// Authorization is decided on these first (write.go): a caller short of the
// admin role must learn nothing about the server's filesystem from the answer,
// so no check runs for them.
func execPathSets(before config.Config, u config.Update) map[string]string {
	out := map[string]string{}
	for head, row := range execPathRows {
		raw, ok := u.Set[head]
		if !ok {
			continue
		}
		var v string
		if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v) == "" {
			continue // not a string: the field check says so; empty: the managed copy
		}
		if v == row.cur(before) {
			continue
		}
		out[head] = v
	}
	return out
}

// execPathHeads are execPathSets' rows, sorted.
func execPathHeads(before config.Config, u config.Update) []string {
	return slices.Sorted(maps.Keys(execPathSets(before, u)))
}

// pathChecker is checkExecPath; a variable so a test can prove that a refused
// caller never reaches the filesystem.
// execLog carries the OS detail a refusal keeps out of its response.
var execLog = logger.New("appconfig")

var pathChecker = checkExecPath

// execPathViolations are the refusals the update's executable-path values earn, as
// field errors naming the key. An empty value (use the managed copy) and a key
// being cleared are always allowed, and so is a value the write leaves unchanged.
// Only an authorized caller reaches it. The error text names the rule that failed
// and never the operating system's own message: the detail is logged here.
func execPathViolations(before config.Config, u config.Update) []error {
	var out []error
	for head, v := range execPathSets(before, u) {
		if err := pathChecker(v, execPathRows[head].kind); err != nil {
			out = append(out, &coreconfig.FieldError{Path: coreconfig.Path(head), Err: err})
		}
	}
	slices.SortFunc(out, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return out
}

// checkExecPath is the rule for one value; the error names no secret and no more
// than the value the caller sent.
func checkExecPath(p string, kind execPathKind) error {
	if !filepath.IsAbs(p) {
		return errors.New("must be an absolute path")
	}
	if filepath.Clean(p) != p {
		return errors.New("must be a clean absolute path (no '..', '.', or doubled or trailing slashes)")
	}
	info, err := os.Stat(p) // follows a symlink: the target is what runs
	switch {
	case errors.Is(err, os.ErrNotExist) && kind == execDir:
		return checkDirOf(filepath.Dir(p))
	case err != nil:
		execLog.Warn("config: executable path %s refused: %s", logger.SanitizeLogValue(p), logger.SanitizeLogValue(err.Error()))
		return errors.New("cannot be used")
	}
	switch kind {
	case execFile:
		if !info.Mode().IsRegular() {
			return errors.New("must be a regular file")
		}
		if info.Mode().Perm()&0o111 == 0 {
			return errors.New("must be executable")
		}
	case execDir:
		if !info.IsDir() {
			return errors.New("must be a directory")
		}
	}
	if info.Mode().Perm()&0o002 != 0 {
		return errors.New("must not be writable by everyone")
	}
	if kind == execFile {
		return checkDirOf(filepath.Dir(p))
	}
	return nil
}

// checkDirOf refuses a directory anyone could put an entry in: world-writable, the
// sticky bit or not (sticky stops a swap of someone else's file, not a file the
// attacker made first).
func checkDirOf(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		execLog.Warn("config: executable path directory %s refused: %s", logger.SanitizeLogValue(dir), logger.SanitizeLogValue(err.Error()))
		return errors.New("its directory cannot be used")
	}
	if info.Mode().Perm()&0o002 != 0 {
		return errors.New("its directory must not be writable by everyone")
	}
	return nil
}
