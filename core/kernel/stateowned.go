// file: core/kernel/stateowned.go
// version: 0.1.0
// guid: 4f0d2b7a-91c3-4e68-a5d7-c18e63b02f94
// last-edited: 2026-11-22

package kernel

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// coreOwned answers one question for Deps.State: is a path something core owns
// in the state directory or beside the store, so that a module's start hook
// removing it (or a directory holding it) would destroy data? Every name it
// uses comes from the package that writes the file, so a rename there moves
// the answer with it:
//
//   - the store at core.storage.path, its sidecars (core's and the build's
//     LegacyGuard suffixes), the swap lock, restore/ and the private swap
//     directories beside the store: restorejournal.Kept, the rule the reset
//     hook's confined filesystem applies (05-storage §5S.3.15 "Hook
//     confinement", rule (4));
//   - the checkpoints (storage.CheckpointRoot);
//   - in the state directory: the keyring, its export record and their
//     temporary files (secrets.OwnsStateName), the config boot record
//     (config.OwnsStateName), the bootstrap token (auth.BootstrapTokenFile)
//     and backup.location.json (journal.LocationFile), each with a prefix
//     match that takes in the temporary file it is staged through.
//
// App-declared state files (Config.StorageStateFiles, 05-config §2.4's table)
// are not core's and are not listed: an app's own legacy file is what a hook
// removes. The revocation journal is bound at run time to a path core does not
// know here, and is not covered.
type coreOwned struct {
	stateDir, dbPath, keyFile string
	legacy                    []string
}

// newCoreOwned is the rule for the store at dbPath under stateDir. keyFile is
// core.secrets.key_file ("" is the default name); owners are the build's
// storage owners, whose LegacyGuard sidecars Kept lists.
func newCoreOwned(stateDir, dbPath, keyFile string, owners []storage.OwnerSpec) *coreOwned {
	return &coreOwned{stateDir: filepath.Clean(stateDir), dbPath: filepath.Clean(dbPath), keyFile: keyFile,
		legacy: restoreswap.LegacySuffixes(owners)}
}

// resolved is the same rule over the real locations of the state directory and
// the store, so a path reached through a symbolic link that stays inside the
// state directory is judged by where it lands.
func (o *coreOwned) resolved() *coreOwned {
	r := *o
	r.stateDir, r.dbPath = realPath(o.stateDir), realPath(o.dbPath)
	return &r
}

// owns reports whether p (absolute, cleaned) is, lies inside or contains
// something core owns.
func (o *coreOwned) owns(p string) bool {
	if restorejournal.Kept(o.stateDir, restorejournal.Intent{DBPath: o.dbPath}, o.legacy, p) {
		return true
	}
	if cp := storage.CheckpointRoot(o.dbPath, ""); restorejournal.InTree(p, cp) || restorejournal.InTree(cp, p) {
		return true
	}
	if !restorejournal.InTree(p, o.stateDir) {
		return false
	}
	rel, err := filepath.Rel(o.stateDir, p)
	if err != nil {
		return true // cannot say: refuse
	}
	return config.OwnsStateName(rel) || secrets.OwnsStateName(o.keyFile, rel) ||
		strings.HasPrefix(rel, auth.BootstrapTokenFile) || strings.HasPrefix(rel, journal.LocationFile)
}

// viaParent is p with the symbolic links of its directory resolved and its last
// element left as it is: removing a link removes the link, never what it points
// at, so a link to the store is not the store.
func viaParent(p string) string {
	p = filepath.Clean(p)
	return filepath.Join(realPath(filepath.Dir(p)), filepath.Base(p))
}

// realPath is p with every symbolic link of its longest existing prefix
// resolved, and the rest as given.
func realPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	} else if !errors.Is(err, fs.ErrNotExist) {
		return p
	}
	dir := filepath.Dir(p)
	if dir == p {
		return p
	}
	return filepath.Join(realPath(dir), filepath.Base(p))
}
