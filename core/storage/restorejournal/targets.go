// file: core/storage/restorejournal/targets.go
// version: 1.0.0
// guid: 00669992-4809-4db9-acd1-5057be6e6488
// last-edited: 2026-11-08

package restorejournal

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// ErrTargetsRefused: an intent's targets have a shape no swap may take
// (05-storage §5S.3.15 "Stage"; R1-21 review m4, m5). Stage refuses such an
// intent before anything is written, and P0a's step a rejects one a stager
// wrote without Stage before anything moves.
var ErrTargetsRefused = errors.New("storage: an intent's targets are refused")

// ValidateTargets checks the shape of an intent's targets by path alone,
// before Validate probes them:
//   - every target path is absolute;
//   - a restore places something at DBPath, and a reset sets DBPath aside,
//     each reset target to its SetAsideDest (the one rule);
//   - no two targets share a live path: a restore's placements, a reset's
//     set-asides;
//   - no live path (a restore's set-aside sources, which are DBPath, its
//     sidecars and every placement's To; a reset's every From) is, holds or
//     lies inside restore/, or holds the state directory, since the slot
//     and the swap's journal would move with it;
//   - no live path lies inside a swap's private directory (a path element
//     .mediacore-restore) or holds one, either the DB parent's or the one
//     beside any live path, where previous/ (the only undo) lives. This
//     also refuses one live path inside another, because the inner one's
//     private directory lies inside the outer one;
//   - a restore's staged item (From) lies outside restore/ and does not hold
//     the state directory. It lies inside the swap's private directory by
//     design (StagingDir).
//
// Every refusal is listed, each wrapping ErrTargetsRefused.
func ValidateTargets(stateDir string, in Intent, legacySuffixes []string) error {
	var errs []error
	refuse := func(path, why string) { errs = append(errs, fmt.Errorf("%w: %s %s", ErrTargetsRefused, path, why)) }
	db := filepath.Clean(in.DBPath)
	slot := Dir(stateDir)
	for _, t := range in.Targets {
		for _, p := range []string{t.From, t.To} {
			if !filepath.IsAbs(p) {
				refuse(p, "is not absolute")
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	seen := map[string]bool{}
	dbMoved := false
	var live []string
	if in.Kind == KindReset {
		for _, t := range in.Targets {
			p := filepath.Clean(t.From)
			if seen[p] {
				refuse(t.From, "is set aside twice")
				continue
			}
			seen[p] = true
			dbMoved = dbMoved || p == db
			live = append(live, p)
			if want := SetAsideDest(stateDir, db, in.ID, p); filepath.Clean(t.To) != want {
				refuse(t.From, fmt.Sprintf("is set aside to %s, want %s (the one set-aside rule)", t.To, want))
			}
		}
		if !dbMoved {
			refuse(in.DBPath, "is not set aside; a reset sets its store aside")
		}
	} else {
		for _, t := range in.Targets {
			to := filepath.Clean(t.To)
			if seen[to] {
				refuse(t.To, "is placed twice")
			}
			seen[to] = true
			dbMoved = dbMoved || to == db
			if from := filepath.Clean(t.From); InTree(from, slot) || InTree(stateDir, from) {
				refuse(t.From, "is a staged item inside restore/ or holding the state directory")
			}
		}
		if !dbMoved {
			refuse(in.DBPath, "has no placement; a restore places a store at its DB path")
		}
		for _, a := range SetAsides(stateDir, in, legacySuffixes) {
			live = append(live, filepath.Clean(a.From))
		}
	}
	roots := []string{filepath.Join(filepath.Dir(db), asideDir)}
	for _, p := range live {
		if r := filepath.Join(filepath.Dir(p), asideDir); !slices.Contains(roots, r) {
			roots = append(roots, r)
		}
	}
	for _, p := range live {
		switch {
		case InTree(p, slot) || InTree(stateDir, p):
			refuse(p, "is, holds or lies inside restore/, or holds the state directory: the slot and the swap's journal would move")
		case inAside(p):
			refuse(p, "lies inside a swap's private directory "+asideDir)
		default:
			for _, r := range roots {
				if InTree(r, p) {
					refuse(p, "holds "+r+", where a swap keeps its undo")
					break
				}
			}
		}
	}
	return errors.Join(errs...)
}

// inAside reports a path element of p named .mediacore-restore.
func inAside(p string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(filepath.Clean(p)), "/"), asideDir)
}
