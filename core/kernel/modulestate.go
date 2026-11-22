// file: core/kernel/modulestate.go
// version: 0.1.0
// guid: b3a9d1e7-64c2-4f58-8d0b-7e21c5f93a46
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/statefs"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
)

// This file is what the kernel puts in module.Deps.State and
// module.Deps.SecretsKeyringExported (R3-03a; 06 §6.4.4, §6.5.1's writer
// table "Run 2 (R3+)"): the two seams a legacy-file-cleanup start hook
// needs and no module could reach before, because core/internal/statefs is
// internal and the core.secrets health Result carries one Reason.

// moduleState is Deps.State: module.StateFS over core/internal/statefs. It
// holds the boot's Seams (read from the context New received, since the
// kit's Run context carries none), the directories it confines a path to, and
// no file: every call opens a statefs.Dir over the root that holds the path,
// so an *os.Root refuses a name that leaves it, ".." or a symbolic link, and
// the crash-matrix kit's filesystem is the one used under its seam.
type moduleState struct {
	roots []string // cleaned, absolute: the state directory, <db parent>
	seams *bootseam.Seams
	// owned says what core owns under the roots (stateowned.go); nil with no
	// store open, where there are no roots either. real is its form over the
	// real locations, for a path reached through a symbolic link; nil under a
	// boot seam, whose filesystem has no links to resolve.
	owned, real *coreOwned
}

var _ module.StateFS = (*moduleState)(nil)

// newModuleState is the handle over roots (a relative or empty one is
// dropped: a handle with no root refuses every path) under seams (nil in
// production).
func newModuleState(seams *bootseam.Seams, owned *coreOwned, roots ...string) *moduleState {
	s := &moduleState{seams: seams, owned: owned}
	if owned != nil && seams == nil {
		s.real = owned.resolved()
	}
	for _, r := range roots {
		if filepath.IsAbs(r) {
			s.roots = append(s.roots, filepath.Clean(r))
		}
	}
	return s
}

// moduleState is the Deps.State of this boot: confined to the state directory
// and <db parent> (05-storage §5S.3.15 "Hook confinement", the reset hook's
// roots), and to nothing where no store is open, which is app.Describe: it
// does no I/O and its modules' Register must not either.
func (k *Kernel) moduleState(ctx context.Context) module.StateFS {
	if k.store == nil || k.store.db == nil {
		return newModuleState(bootseam.From(ctx), nil)
	}
	roots := []string{k.cfg.StateDir}
	if k.cfg.StoragePath != "" {
		roots = append(roots, filepath.Dir(k.cfg.StoragePath))
	}
	keyFile := ""
	if k.sec != nil {
		keyFile = k.sec.store.KeyFile()
	}
	return newModuleState(bootseam.From(ctx), newCoreOwned(k.cfg.StateDir, k.cfg.StoragePath, keyFile, k.storageOwners()), roots...)
}

// resolve is path's root and its name under it. A path that is relative, that
// is no root's descendant or that is a root itself is ErrStateConfined; the
// innermost root wins, so the same Dir serves whichever of two nested roots
// the caller thinks of.
func (s *moduleState) resolve(label, path string) (root, rel string, err error) {
	if label == "" {
		return "", "", module.ErrStateUnlabelled
	}
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("%w: %q is not absolute", module.ErrStateConfined, path)
	}
	path = filepath.Clean(path)
	for _, r := range s.roots {
		n, err := filepath.Rel(r, path)
		if err != nil || n == "." || n == ".." || strings.HasPrefix(n, ".."+string(filepath.Separator)) {
			continue
		}
		if root == "" || len(r) > len(root) {
			root, rel = r, n
		}
	}
	if root == "" {
		return "", "", fmt.Errorf("%w: %s", module.ErrStateConfined, path)
	}
	if s.owned != nil && (s.owned.owns(path) || s.real != nil && s.real.owns(viaParent(path))) {
		return "", "", fmt.Errorf("%w: %s", module.ErrStateCoreOwned, path)
	}
	return root, rel, nil
}

// Remove implements module.StateFS.
func (s *moduleState) Remove(ctx context.Context, label, path string) error {
	return s.remove(ctx, label, path, false)
}

// RemoveAll implements module.StateFS.
func (s *moduleState) RemoveAll(ctx context.Context, label, path string) error {
	return s.remove(ctx, label, path, true)
}

// remove is the one sequence of both calls: the entry's Lstat (an absent one
// is done, with no operation and so no kill point for the kit to break), the
// removal under label, then the parent directory's fsync under the same label,
// so the removal is durable when the call returns.
func (s *moduleState) remove(ctx context.Context, label, path string, all bool) (err error) {
	root, rel, err := s.resolve(label, path)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := statefs.OpenSeams(s.seams, root)
	if err != nil {
		if all && errors.Is(err, fs.ErrNotExist) {
			return nil // no directory, so nothing under it
		}
		return err
	}
	// A failed close of the directory handle is reported too, after the
	// removal it follows: the next call finds the path gone and is done.
	defer func() { err = errors.Join(err, d.Close()) }()
	if _, err := d.Lstat(rel); err != nil {
		if all && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if all {
		err = d.RemoveAll(label, rel)
	} else {
		err = d.Remove(label, rel)
	}
	if err != nil {
		return err
	}
	return d.SyncDir(label, filepath.Dir(rel))
}

// keyringExported is Deps.SecretsKeyringExported: keyringExportedQuery over the
// secret store this boot opened, bound now (P6) and not read through k.sec at
// each call, which closeSecrets sets to nil at shutdown while a hook's
// goroutine may still ask. nil where no secret store is open.
func (k *Kernel) keyringExported() func() bool {
	if k.sec == nil {
		return keyringExportedQuery(nil)
	}
	return keyringExportedQuery(k.sec.store)
}

// keyringExportedQuery is the query over store: true only when
// the keyring is open and every key of it is in an export. It asks the store
// (the predicate core.secrets' health report uses, keyringNotExported), not
// the health Result, which names one Reason, most urgent first, so
// secrets_undecryptable can hide secrets_keyring_not_exported, and which
// reads Up until P4c's first report. A nil store (no secret store was
// opened: app.Describe) and a closed one are not exported.
func keyringExportedQuery(store *secrets.Store) func() bool {
	return func() bool {
		return store != nil && keyringNotExported(store) == ""
	}
}
