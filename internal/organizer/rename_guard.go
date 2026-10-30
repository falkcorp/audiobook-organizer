// file: internal/organizer/rename_guard.go
// version: 1.1.0
// guid: 2b8f4d61-7c3e-4a95-b1d0-9e6a5c38f147
// last-edited: 2026-10-29

package organizer

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ErrProtectedRenameSource reports a rename plan that would move a protected
// file (a Deluge seeding file, the iTunes library). RenameFiles refuses the
// whole plan before anything moves.
var ErrProtectedRenameSource = errors.New("rename plan would move a protected file")

// ErrRenameProtectedListNotLoaded reports a rename plan held back because the
// Deluge seeding list has not loaded, so whether a source is a seeding file is
// not known. Nothing moved; the plan is fine and is accepted once the list has
// loaded. It is not ErrProtectedRenameSource: no file in the plan was found to
// be protected.
var ErrRenameProtectedListNotLoaded = errors.New("rename plan waits for the Deluge seeding list to load")

// ProtectedListNotLoadedReason is what a move held back for an unloaded Deluge
// list says, wherever it says it (DestinationConflictError.Reason, the
// RenameFiles error).
const ProtectedListNotLoadedReason = "the Deluge seeding list has not loaded yet; try again once it has"

// SourceVerdict is a rename-source guard's answer about one source.
type SourceVerdict int

const (
	// SourceMovable: nothing protects the source and the list that could have
	// said so is loaded.
	SourceMovable SourceVerdict = iota
	// SourceProtected: the source is protected (iTunes, an import folder, the
	// quarantine folder, a seeding path, directly or through a symlink).
	SourceProtected
	// SourceListNotLoaded: nothing protects the source as far as the loaded
	// lists know, but the Deluge seeding list has not loaded, and an unloaded list
	// reports every seeding file unprotected. Refused too (fail closed), under
	// its own reason: the source is not known to be protected, and the fix is to
	// wait for the list, not to look at the file.
	SourceListNotLoaded
)

// renameSourceGuard classifies a rename source. Nil (the default, and in tests
// that do not set it) protects nothing.
var renameSourceGuard atomic.Pointer[func(string) SourceVerdict]

// SetRenameSourceGuard installs the predicate RenameFiles and the in-place
// organize consult for every source they would move. The server wires the
// protected-path check, including the Deluge save paths, and answers
// SourceListNotLoaded for a source nothing protects while the Deluge list has
// not loaded. A source that IS protected must answer SourceProtected even then:
// the reason that holds for good is the one to report. Returns a func restoring
// the previous guard.
//
// Until 2026-09-14 RenameFiles had no protected-path check of its own: the
// callers filtered with checks that did not know the Deluge save paths, so a
// seeding file reached os.Rename. Until 2026-10-29 the guard was a bool, which
// folded "the Deluge list has not loaded" into "protected", so every in-place
// move was reported as sitting in a protected location while Deluge was down.
func SetRenameSourceGuard(fn func(string) SourceVerdict) (restore func()) {
	var p *func(string) SourceVerdict
	if fn != nil {
		p = &fn
	}
	prev := renameSourceGuard.Swap(p)
	return func() { renameSourceGuard.Store(prev) }
}

// classifyRenameSource asks the installed guard about src; SourceMovable when
// there is none.
func classifyRenameSource(src string) SourceVerdict {
	g := renameSourceGuard.Load()
	if g == nil {
		return SourceMovable
	}
	return (*g)(src)
}

// listNotLoadedWarnEvery is how often a refusal for the unloaded Deluge list is
// logged at Warn by RenameFiles, which can be called once per book. Not a const
// so tests can shrink it.
var listNotLoadedWarnEvery = time.Minute

var (
	listNotLoadedWarnMu   sync.Mutex
	listNotLoadedWarnLast time.Time
	// renameClock is the time source of the throttle, replaceable by tests.
	renameClock = time.Now
)

// shouldWarnListNotLoaded says whether RenameFiles should log a refusal for the
// unloaded Deluge list now: the first, then at most one per
// listNotLoadedWarnEvery.
func shouldWarnListNotLoaded() bool {
	listNotLoadedWarnMu.Lock()
	defer listNotLoadedWarnMu.Unlock()
	now := renameClock()
	if !listNotLoadedWarnLast.IsZero() && now.Sub(listNotLoadedWarnLast) < listNotLoadedWarnEvery {
		return false
	}
	listNotLoadedWarnLast = now
	return true
}

// guardedRenameSources sorts the sources in entries the guard objects to into
// the protected ones and the ones held only for the unloaded list, skipping
// entries already at their target (nothing moves for those).
func guardedRenameSources(entries []FileRenameEntry) (protected, listNotLoaded []string) {
	if renameSourceGuard.Load() == nil {
		return nil, nil
	}
	for _, e := range entries {
		if filepath.Clean(e.SourcePath) == filepath.Clean(e.TargetPath) {
			continue
		}
		switch classifyRenameSource(e.SourcePath) {
		case SourceProtected:
			protected = append(protected, e.SourcePath)
		case SourceListNotLoaded:
			listNotLoaded = append(listNotLoaded, e.SourcePath)
		}
	}
	return protected, listNotLoaded
}
