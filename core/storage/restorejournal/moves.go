// file: core/storage/restorejournal/moves.go
// version: 1.0.1
// guid: a4401f6b-1776-403b-8b7b-8b40bb883e63
// last-edited: 2026-11-08

package restorejournal

import (
	"path/filepath"
	"slices"
	"strings"
)

// The sidecar suffixes every core store has beside its DB path (05-storage
// §5S.3.7 "Level sidecar", §5S.3.12 "How"). An owner's legacy projection
// sidecar (books' ".storage-format") is the caller's: it comes from the
// build's LegacyGuard declarations.
const (
	LevelsSuffix     = ".storage-levels"
	CheckpointSuffix = ".migration-checkpoint"
)

// CoreSidecarSuffixes is LevelsSuffix and CheckpointSuffix.
func CoreSidecarSuffixes() []string { return []string{LevelsSuffix, CheckpointSuffix} }

// The private directory a swap keeps beside a target's filesystem root, and
// its two leaves: previous/ holds what step c (or a reset's set-aside) moved
// away, failed/ what step x moved out of the live paths.
const (
	asideDir  = ".mediacore-restore"
	leafPrev  = "previous"
	leafFail  = "failed"
	leafStage = "staging"
)

// AsideDirName is the private directory's name: a path with an element of
// this name lies in some swap's private namespace, which held nothing
// before the swap made it.
const AsideDirName = asideDir

// SetAsideDest is the one set-aside rule of 05-storage §5S.3.15 "Stage"
// (06 P0a cites it, COR9-12): a target under the state directory goes to
// <stateDir>/restore/<id>/previous/, a target under the DB's parent to
// <db parent>/.mediacore-restore/<id>/previous/, and every other target
// beside itself, to <its parent>/.mediacore-restore/<id>/previous/. Under
// the first two the path relative to that root is kept, so two targets with
// one base name never collide.
//
// The rule is by path. "On its filesystem" is Validate's: a target that
// would land on another filesystem than its own is refused at staging with
// ErrRenameRefused, never relocated.
func SetAsideDest(stateDir, dbPath, id, target string) string {
	return asideDest(stateDir, dbPath, id, target, leafPrev)
}

// FailedDest is step x's destination for a live item that is not the
// original (the staged item, or a sidecar or pointer the restore boot
// wrote): SetAsideDest's rule with failed/ in place of previous/, so it
// stays on the item's own filesystem (decision D3 of R1-21; 05-backup step x
// names restore/<id>/failed/, which is under the state directory and fails
// with EXDEV for a DB elsewhere).
func FailedDest(stateDir, dbPath, id, target string) string {
	return asideDest(stateDir, dbPath, id, target, leafFail)
}

// StagingDir is <db parent>/.mediacore-restore/<id>/staging, where a
// restore's stager extracts what step d places (05-backup §5B.3.4 step 7).
func StagingDir(dbPath, id string) string {
	return filepath.Join(filepath.Dir(dbPath), asideDir, id, leafStage)
}

// stateLeaf is the directory under a staging directory that holds a
// restore's staged state files, one per label, apart from the store an
// archive extracts into staging/db.
const stateLeaf = "state"

// StagingDest is where a restore's stager stages the file that step d
// renames onto target, label its state-file label (one path segment): the
// twin of SetAsideDest, so the staged file lies on target's own filesystem
// and the rename passes Validate. A target under the state directory is
// staged at <stateDir>/.mediacore-restore/<id>/staging/state/<label>, a
// target under the DB's parent at <db parent>/.mediacore-restore/<id>/
// staging/state/<label> (StagingDir's state/), and every other target beside
// itself, at <its parent>/.mediacore-restore/<id>/staging/state/<label>. The
// state directory's first, as SetAsideDest's rule reads it.
//
// The state directory's staging is its private directory, not restore/<id>:
// a staged item inside restore/ is refused (ValidateTargets), Stage refuses
// an id whose restore/<id> exists, and an escape stages under a fresh id but
// journals under the failed restore's.
//
// A packaged install keeps its state directory (/var/lib/app) on one
// filesystem and its store on another; staging every state file beside the
// store made the rename onto each one cross filesystems, and the checkpoint
// restore, the only way back after an upgrade, was refused at staging.
func StagingDest(stateDir, dbPath, id, target, label string) string {
	return filepath.Join(stagingRoot(stateDir, dbPath, id, target), leafStage, stateLeaf, label)
}

// stagingRoot is the swap's private directory of id that StagingDest stages
// target in.
func stagingRoot(stateDir, dbPath, id, target string) string {
	switch {
	case under(target, stateDir):
		return filepath.Join(filepath.Clean(stateDir), asideDir, id)
	case under(target, filepath.Dir(filepath.Clean(dbPath))):
		return filepath.Join(filepath.Dir(filepath.Clean(dbPath)), asideDir, id)
	}
	return filepath.Join(filepath.Dir(filepath.Clean(target)), asideDir, id)
}

// AsideRoots are the private directories a swap of id may create, its
// stager's staging included: the state directory's restore/<id>, the DB
// parent's .mediacore-restore/<id>, the state directory's
// .mediacore-restore/<id> when a target lies under it (StagingDest), and one
// beside each target outside both. The swap's own bookkeeping goes in the
// first; every SetAsideDest, FailedDest and StagingDest of the targets lies
// in one of them.
func AsideRoots(stateDir, dbPath, id string, targets []string) []string {
	out := []string{JournalDir(stateDir, id), filepath.Join(filepath.Dir(dbPath), asideDir, id)}
	for _, t := range targets {
		if under(t, stateDir) || !under(t, filepath.Dir(dbPath)) {
			if r := stagingRoot(stateDir, dbPath, id, t); !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// StagingRoots are the private directories a restore's stager of id stages
// into for targets (each StagingDest's, and StagingDir's for the store): the
// aside roots outside restore/, which Stage alone creates. Every one holds
// only what this stager wrote until Stage links the intent, since id is
// fresh, so a stager that is refused before then removes each whole.
func StagingRoots(stateDir, dbPath, id string, targets []string) []string {
	var out []string
	for _, r := range AsideRoots(stateDir, dbPath, id, targets) {
		if r != JournalDir(stateDir, id) {
			out = append(out, r)
		}
	}
	return out
}

func asideDest(stateDir, dbPath, id, target, leaf string) string {
	target = filepath.Clean(target)
	dbParent := filepath.Dir(filepath.Clean(dbPath))
	if r, ok := inside(target, stateDir); ok {
		return filepath.Join(JournalDir(stateDir, id), leaf, r)
	}
	if r, ok := inside(target, dbParent); ok {
		return filepath.Join(dbParent, asideDir, id, leaf, r)
	}
	return filepath.Join(filepath.Dir(target), asideDir, id, leaf, filepath.Base(target))
}

// under reports p strictly inside dir.
func under(p, dir string) bool {
	_, ok := inside(p, dir)
	return ok
}

// inside is p relative to dir when p lies strictly inside dir.
func inside(p, dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	r, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", false
	}
	return r, true
}

// SetAsides is a restore intent's set-aside list, step c of 05-backup
// §5B.3.4 (decision D1 of R1-21): the intent's Targets are the placements of
// step d ({From: staged item, To: live path}), and the live items they
// replace are derived here, so a stager and the swapper compute one list.
// In order: the DB directory, its sidecars (dbPath + each core suffix, then
// each of legacySuffixes, the build's LegacyGuard sidecars), then every
// other Target.To, each From the live path and To its SetAsideDest. A
// KindReset intent's Targets are already its set-aside pairs and it has no
// derived list.
func SetAsides(stateDir string, in Intent, legacySuffixes []string) []Target {
	db := filepath.Clean(in.DBPath)
	seen := map[string]bool{}
	var out []Target
	add := func(p string) {
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, Target{From: p, To: SetAsideDest(stateDir, db, in.ID, p)})
	}
	add(db)
	suffixes := append(CoreSidecarSuffixes(), legacySuffixes...)
	for _, s := range suffixes {
		add(db + s)
	}
	for _, t := range in.Targets {
		add(t.To)
	}
	return out
}
