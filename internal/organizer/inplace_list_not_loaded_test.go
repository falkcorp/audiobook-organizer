// file: internal/organizer/inplace_list_not_loaded_test.go
// version: 1.1.0
// guid: a217e80d-6a0c-49a9-ac4c-2d4b435b7c7c
// last-edited: 2026-10-30

package organizer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// listState is a rename-source guard whose Deluge list can be loaded or not,
// the way the server's is: a protected source is protected either way, any other
// source waits for the list while it is down.
type listState struct {
	loaded    atomic.Bool
	protected string // a folder every path under which is protected
}

func (g *listState) verdict(p string) SourceVerdict {
	if g.protected != "" && strings.HasPrefix(p, g.protected+string(os.PathSeparator)) {
		return SourceProtected
	}
	if !g.loaded.Load() {
		return SourceListNotLoaded
	}
	return SourceMovable
}

func installListGuard(t *testing.T, g *listState) {
	t.Helper()
	t.Cleanup(SetRenameSourceGuard(g.verdict))
}

// warnRecorder is a noopLogger that keeps what is logged at Warn.
type warnRecorder struct {
	noopLogger
	warns []string
}

func (w *warnRecorder) Warn(format string, args ...any) {
	w.warns = append(w.warns, fmt.Sprintf(format, args...))
}

// With the Deluge list not loaded, an in-place move is held back for THAT reason,
// not reported as sitting in a protected location, and nothing moves. Once the
// list has loaded the same book moves.
func TestReOrganizeInPlace_DelugeListNotLoadedIsItsOwnOutcomeThenMovesOnceLoaded(t *testing.T) {
	svc, store, root := setupInPlace(t)
	g := &listState{}
	installListGuard(t, g)
	src := filepath.Join(root, "incoming", "killing-joke.m4b")
	book := addInPlaceBook(t, store, "joker", "Killing Joke", src, filled(120, 3), nil, 0)

	_, err := svc.ReOrganizeInPlace(book, &noopLogger{})
	var conflict *DestinationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a DestinationConflictError", err)
	}
	if conflict.Category != OutcomeProtectedListNotLoaded {
		t.Fatalf("category = %q, want %q (not %q: the file is not known to be protected)",
			conflict.Category, OutcomeProtectedListNotLoaded, OutcomeProtectedSource)
	}
	if conflict.Reason != ProtectedListNotLoadedReason || !strings.Contains(conflict.Reason, "Deluge seeding list has not loaded") {
		t.Fatalf("reason = %q", conflict.Reason)
	}
	requireStillThere(t, store, book, src, src)

	g.loaded.Store(true)
	newPath, err := svc.ReOrganizeInPlace(book, &noopLogger{})
	if err != nil {
		t.Fatalf("move refused with the list loaded: %v", err)
	}
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Fatalf("book not at its new path %s: %v", newPath, statErr)
	}
}

// A file that IS protected is reported as protected even while the list is down:
// that is the reason that holds for good.
func TestReOrganizeInPlace_ProtectedSourceStaysProtectedWhileTheListIsNotLoaded(t *testing.T) {
	svc, store, root := setupInPlace(t)
	g := &listState{}
	installListGuard(t, g)
	real := filepath.Join(t.TempDir(), "Gotham Music", "Hush", "hush.m4b")
	if err := os.MkdirAll(filepath.Dir(real), 0o775); err != nil {
		t.Fatal(err)
	}
	link := linkedLibraryDir(t, root, filepath.Dir(real))
	g.protected = link // the folder this guard protects, as spelled
	book := addInPlaceBook(t, store, "hush", "Hush", filepath.Join(link, "hush.m4b"), filled(120, 3), nil, 0)

	_, err := svc.ReOrganizeInPlace(book, &noopLogger{})
	var conflict *DestinationConflictError
	if !errors.As(err, &conflict) || conflict.Category != OutcomeProtectedSource {
		t.Fatalf("err = %v, want a %s refusal", err, OutcomeProtectedSource)
	}
}

// A multi-file book is held back the same way, before any file moves.
func TestReOrganizeInPlace_MultiFileDelugeListNotLoadedIsItsOwnOutcome(t *testing.T) {
	svc, store, root := setupInPlace(t)
	g := &listState{}
	installListGuard(t, g)
	book, first, second := multiFileBook(t, store, root, "Knightfall")

	_, err := svc.ReOrganizeInPlace(book, &noopLogger{})
	requireConflict(t, err, OutcomeProtectedListNotLoaded)
	requireStillThere(t, store, book, first, first, second)
}

// A run counts the held-back books as skips under their own key, never as
// failures, and says so once at Warn (the per-book line is Debug).
func TestOrganizeBooks_DelugeListNotLoadedIsCountedAndWarnedOncePerRun(t *testing.T) {
	svc, store, root := setupInPlace(t)
	g := &listState{}
	installListGuard(t, g)
	var books []database.Book
	for i, title := range []string{"Year One", "The Long Halloween", "Hush"} {
		src := filepath.Join(root, "incoming", fmt.Sprintf("book-%d.m4b", i))
		b := addInPlaceBook(t, store, fmt.Sprintf("b%d", i), title, src, filled(100+i, byte(i+1)), nil, 0)
		books = append(books, *b)
	}

	log := &warnRecorder{}
	stats := svc.organizeBooksOpts(context.Background(), books, nil, log, "", false, nil)
	if stats.Collisions[OutcomeProtectedListNotLoaded] != len(books) || stats.Failed != 0 || stats.Skipped != len(books) {
		t.Fatalf("want %d protected_list_not_loaded skips and no failures, got %+v", len(books), stats)
	}
	if stats.Collisions[OutcomeProtectedSource] != 0 {
		t.Fatalf("books were counted as protected_source: %+v", stats.Collisions)
	}
	var listWarns []string
	for _, w := range log.warns {
		if strings.Contains(w, ProtectedListNotLoadedReason) {
			listWarns = append(listWarns, w)
		}
	}
	if len(listWarns) != 1 || !strings.Contains(listWarns[0], fmt.Sprint(len(books))) {
		t.Fatalf("want exactly one Warn naming the %d held-back moves, got %q", len(books), log.warns)
	}
}

// RenameFiles holds a plan back for the unloaded list under its own error, not as
// a protected-file refusal, and nothing moves. A plan that does contain a
// protected file is still the protected refusal.
func TestRenameFiles_DelugeListNotLoadedIsItsOwnError(t *testing.T) {
	dir := t.TempDir()
	seeding := filepath.Join(dir, "seeding")
	lib := filepath.Join(dir, "lib")
	for _, d := range []string{seeding, lib} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	protectedSrc := filepath.Join(seeding, "a.m4b")
	librarySrc := filepath.Join(lib, "b.m4b")
	for _, f := range []string{protectedSrc, librarySrc} {
		if err := os.WriteFile(f, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g := &listState{protected: seeding}
	installListGuard(t, g)
	libEntry := FileRenameEntry{SourcePath: librarySrc, TargetPath: filepath.Join(lib, "B", "b.m4b")}

	res, err := RenameFiles([]FileRenameEntry{libEntry}, nil)
	if !errors.Is(err, ErrRenameProtectedListNotLoaded) || errors.Is(err, ErrProtectedRenameSource) {
		t.Fatalf("err = %v, want ErrRenameProtectedListNotLoaded and not ErrProtectedRenameSource", err)
	}
	if !strings.Contains(err.Error(), ProtectedListNotLoadedReason) || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], ProtectedListNotLoadedReason) {
		t.Fatalf("message = %q, result errors = %q", err, res.Errors)
	}
	if _, statErr := os.Stat(librarySrc); statErr != nil {
		t.Fatalf("source moved: %v", statErr)
	}
	if _, statErr := os.Stat(libEntry.TargetPath); statErr == nil {
		t.Fatal("target created")
	}

	_, err = RenameFiles([]FileRenameEntry{libEntry, {SourcePath: protectedSrc, TargetPath: filepath.Join(lib, "A", "a.m4b")}}, nil)
	if !errors.Is(err, ErrProtectedRenameSource) || errors.Is(err, ErrRenameProtectedListNotLoaded) {
		t.Fatalf("err = %v, want ErrProtectedRenameSource: a protected file wins over the unloaded list", err)
	}

	g.loaded.Store(true)
	if _, err = RenameFiles([]FileRenameEntry{libEntry}, nil); err != nil {
		t.Fatalf("plan refused with the list loaded: %v", err)
	}
	if _, statErr := os.Stat(libEntry.TargetPath); statErr != nil {
		t.Fatalf("file not renamed with the list loaded: %v", statErr)
	}
}

// setListNotLoadedWarnHooks swaps the Warn throttle's clock and interval, and
// forgets the last Warn, under the lock the production code reads them with. The
// returned func restores them.
func setListNotLoadedWarnHooks(clk func() time.Time, every time.Duration) (restore func()) {
	listNotLoadedWarnMu.Lock()
	prevClock, prevEvery, prevLast := renameClock, listNotLoadedWarnEvery, listNotLoadedWarnLast
	renameClock, listNotLoadedWarnEvery, listNotLoadedWarnLast = clk, every, time.Time{}
	listNotLoadedWarnMu.Unlock()
	return func() {
		listNotLoadedWarnMu.Lock()
		renameClock, listNotLoadedWarnEvery, listNotLoadedWarnLast = prevClock, prevEvery, prevLast
		listNotLoadedWarnMu.Unlock()
	}
}

// RenameFiles can be called once per book, so its Warn is throttled.
func TestShouldWarnListNotLoaded_IsThrottled(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	t.Cleanup(setListNotLoadedWarnHooks(func() time.Time { return now }, time.Minute))

	if warn, every := shouldWarnListNotLoaded(); !warn || every != time.Minute {
		t.Fatalf("the first refusal is logged (warn=%v every=%v)", warn, every)
	}
	for range 100 {
		if warn, _ := shouldWarnListNotLoaded(); warn {
			t.Fatal("logged again inside the interval")
		}
	}
	now = now.Add(61 * time.Second)
	if warn, _ := shouldWarnListNotLoaded(); !warn {
		t.Fatal("not logged again after the interval")
	}
}

// The throttle's hooks are read under the lock the setter takes, so swapping them
// while RenameFiles calls run does not race. Meaningful under -race.
func TestShouldWarnListNotLoaded_HooksAreReadUnderTheirLock(t *testing.T) {
	restore := setListNotLoadedWarnHooks(time.Now, time.Millisecond)
	t.Cleanup(restore)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				shouldWarnListNotLoaded()
			}
		}
	}()
	for range 500 {
		r := setListNotLoadedWarnHooks(time.Now, time.Millisecond)
		r()
	}
	close(stop)
	<-done
}

// Only an explicit SourceMovable clears a source. A guard that returns the zero
// value (it decided nothing) or a verdict this code has never heard of is a
// refusal under its own outcome, for the in-place move and for RenameFiles.
func TestRenameSourceGuard_OnlyAnExplicitMovableClearsASource(t *testing.T) {
	for name, verdict := range map[string]SourceVerdict{"zero value": SourceVerdict(0), "unknown value": SourceVerdict(99)} {
		t.Run(name, func(t *testing.T) {
			svc, store, root := setupInPlace(t)
			t.Cleanup(SetRenameSourceGuard(func(string) SourceVerdict { return verdict }))
			src := filepath.Join(root, "incoming", "killing-joke.m4b")
			book := addInPlaceBook(t, store, "joker", "Killing Joke", src, filled(120, 3), nil, 0)

			_, err := svc.ReOrganizeInPlace(book, &noopLogger{})
			var conflict *DestinationConflictError
			if !errors.As(err, &conflict) || conflict.Category != OutcomeUnknownGuardVerdict {
				t.Fatalf("err = %v, want a %s refusal", err, OutcomeUnknownGuardVerdict)
			}
			if !strings.Contains(conflict.Reason, "unknown guard verdict") {
				t.Fatalf("reason = %q", conflict.Reason)
			}
			requireStillThere(t, store, book, src, src)

			dir := t.TempDir()
			f := filepath.Join(dir, "a.m4b")
			if werr := os.WriteFile(f, []byte("audio"), 0o644); werr != nil {
				t.Fatal(werr)
			}
			entry := FileRenameEntry{SourcePath: f, TargetPath: filepath.Join(dir, "A", "a.m4b")}
			_, rerr := RenameFiles([]FileRenameEntry{entry}, nil)
			if !errors.Is(rerr, ErrRenameGuardVerdictUnknown) || errors.Is(rerr, ErrProtectedRenameSource) || errors.Is(rerr, ErrRenameProtectedListNotLoaded) {
				t.Fatalf("err = %v, want ErrRenameGuardVerdictUnknown alone", rerr)
			}
			if _, statErr := os.Stat(f); statErr != nil {
				t.Fatalf("source moved: %v", statErr)
			}
			if _, statErr := os.Stat(entry.TargetPath); statErr == nil {
				t.Fatal("target created")
			}
		})
	}
}

// With no guard installed (tests, tools) nothing is asked and every source is
// movable; only an installed guard has to say SourceMovable.
func TestRenameSourceGuard_NoGuardInstalledProtectsNothing(t *testing.T) {
	t.Cleanup(SetRenameSourceGuard(nil))
	if err := refuseProtectedInPlaceSource(filepath.Join(t.TempDir(), "a.m4b"), "x"); err != nil {
		t.Fatalf("no guard: %v", err)
	}
	if g := guardedRenameSources([]FileRenameEntry{{SourcePath: "/a", TargetPath: "/b"}}); len(g.protected)+len(g.listNotLoaded)+len(g.unknown) != 0 {
		t.Fatalf("no guard objected: %+v", g)
	}
}
