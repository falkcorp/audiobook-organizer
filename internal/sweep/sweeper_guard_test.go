// file: internal/sweep/sweeper_guard_test.go
// version: 1.0.0
// guid: 9d2f6b41-5a83-4c07-b3e9-0c71a8d54e26
// last-edited: 2026-10-30

package sweep

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// guardVerdict is what fakeGuard answers for one path.
type guardVerdict struct {
	remove bool
	err    error
}

// fakeGuard records what the sweep asked it, so the tests can show the sweep
// honours every answer. The rules themselves are tested against the real guard
// in package audiobooks (tombstone_sweep_guard_test.go).
type fakeGuard struct {
	rootErr    error
	rootCalls  int
	verdicts   map[string]guardVerdict
	checked    []string
	stampsSeen map[string]*database.TombstoneFileStamp
}

func (g *fakeGuard) RootReady() error { g.rootCalls++; return g.rootErr }

func (g *fakeGuard) Check(path string, stamp *database.TombstoneFileStamp) (bool, error) {
	g.checked = append(g.checked, path)
	if g.stampsSeen == nil {
		g.stampsSeen = map[string]*database.TombstoneFileStamp{}
	}
	g.stampsSeen[path] = stamp
	v, ok := g.verdicts[path]
	if !ok {
		return false, errors.New("fakeGuard: no verdict configured")
	}
	return v.remove, v.err
}

// stampedStore adds the stamp reader capability to MockBookStore.
type stampedStore struct {
	*MockBookStore
	stamps   map[string]*database.TombstoneFileStamp
	stampErr error
}

func (s *stampedStore) GetBookTombstoneStamp(id string) (*database.TombstoneFileStamp, error) {
	if s.stampErr != nil {
		return nil, s.stampErr
	}
	return s.stamps[id], nil
}

func writeSweepFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestSweepTombstones_GuardDecidesTheFile(t *testing.T) {
	stamp := &database.TombstoneFileStamp{Size: 5, MtimeNS: 1}
	cases := []struct {
		name         string
		guard        func(path string) *fakeGuard // nil result = nil guard
		stampErr     error
		withStamp    bool
		wantFile     bool // file still exists afterwards
		wantTomb     bool // tombstone still exists afterwards
		wantCleaned  int
		wantRefused  int
		wantErrors   int
		wantRootAsks int
	}{
		{
			name:        "nil guard refuses every removal",
			guard:       func(string) *fakeGuard { return nil },
			wantFile:    true,
			wantTomb:    true,
			wantRefused: 1,
		},
		{
			name: "library root not ready keeps file and tombstone",
			guard: func(string) *fakeGuard {
				return &fakeGuard{rootErr: errors.New("library not mounted")}
			},
			wantFile: true, wantTomb: true, wantRefused: 1, wantRootAsks: 1,
		},
		{
			name: "guard refusal (outside root, protected, referenced, stamp mismatch) keeps both",
			guard: func(p string) *fakeGuard {
				return &fakeGuard{verdicts: map[string]guardVerdict{p: {err: errors.New("is outside the library root")}}}
			},
			withStamp: true, wantFile: true, wantTomb: true, wantRefused: 1, wantRootAsks: 1,
		},
		{
			name: "unreadable stamp is a refusal",
			guard: func(p string) *fakeGuard {
				return &fakeGuard{verdicts: map[string]guardVerdict{p: {remove: true}}}
			},
			stampErr: errors.New("pebble: io error"),
			wantFile: true, wantTomb: true, wantRefused: 1, wantRootAsks: 1,
		},
		{
			name: "guard says nothing is there: tombstone cleaned, nothing unlinked",
			guard: func(p string) *fakeGuard {
				return &fakeGuard{verdicts: map[string]guardVerdict{p: {remove: false}}}
			},
			wantFile: true, wantTomb: false, wantCleaned: 1, wantRootAsks: 1,
		},
		{
			name: "guard allows: file removed and tombstone deleted",
			guard: func(p string) *fakeGuard {
				return &fakeGuard{verdicts: map[string]guardVerdict{p: {remove: true}}}
			},
			withStamp: true, wantFile: false, wantTomb: false, wantCleaned: 1, wantRootAsks: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSweepFile(t, t.TempDir(), "dark-knight-returns.m4b")
			base := &MockBookStore{tombstones: []database.Book{{ID: "dkr", FilePath: path}}}
			store := &stampedStore{MockBookStore: base, stampErr: tc.stampErr}
			if tc.withStamp {
				store.stamps = map[string]*database.TombstoneFileStamp{"dkr": stamp}
			}
			g := tc.guard(path)
			var guard FileGuard // keep a nil interface nil
			if g != nil {
				guard = g
			}

			res, err := SweepTombstones(store, guard)
			if err != nil {
				t.Fatal(err)
			}
			if got := exists(path); got != tc.wantFile {
				t.Errorf("file exists = %v, want %v", got, tc.wantFile)
			}
			if got := len(base.tombstones) == 1; got != tc.wantTomb {
				t.Errorf("tombstone kept = %v, want %v", got, tc.wantTomb)
			}
			if res.TombstonesCleaned != tc.wantCleaned || res.RefusedFiles != tc.wantRefused || len(res.Errors) != tc.wantErrors {
				t.Errorf("result = %+v, want cleaned=%d refused=%d errors=%d", res, tc.wantCleaned, tc.wantRefused, tc.wantErrors)
			}
			if len(res.Refusals) != tc.wantRefused {
				t.Errorf("Refusals = %v, want %d entries", res.Refusals, tc.wantRefused)
			}
			if g != nil {
				if g.rootCalls != tc.wantRootAsks {
					t.Errorf("RootReady asked %d times, want %d", g.rootCalls, tc.wantRootAsks)
				}
				if tc.withStamp && g.stampsSeen[path] != stamp {
					t.Errorf("guard saw stamp %v, want the tombstone's %v", g.stampsSeen[path], stamp)
				}
			}
		})
	}
}

// The library root is asked once per sweep, however many tombstones name files,
// and a book that is still live never reaches the guard.
func TestSweepTombstones_RootAskedOnceAndLiveBookSkipsGuard(t *testing.T) {
	dir := t.TempDir()
	a := writeSweepFile(t, dir, "a.m4b")
	b := writeSweepFile(t, dir, "b.m4b")
	base := &MockBookStore{
		tombstones: []database.Book{{ID: "a", FilePath: a}, {ID: "b", FilePath: b}, {ID: "live", FilePath: "/never/asked.m4b"}},
		books:      []database.Book{{ID: "live", Title: "Batman: Year One"}},
	}
	g := &fakeGuard{verdicts: map[string]guardVerdict{a: {remove: true}, b: {remove: true}}}
	res, err := SweepTombstones(base, g)
	if err != nil {
		t.Fatal(err)
	}
	if g.rootCalls != 1 {
		t.Errorf("RootReady asked %d times, want 1", g.rootCalls)
	}
	if len(g.checked) != 2 {
		t.Errorf("guard asked about %v, want only the two orphaned paths", g.checked)
	}
	if res.TombstonesCleaned != 3 || res.RefusedFiles != 0 {
		t.Errorf("result = %+v, want 3 cleaned, 0 refused", res)
	}
}

// A tombstone with no file path has nothing to guard.
func TestSweepTombstones_NoFilePathNeedsNoGuard(t *testing.T) {
	base := &MockBookStore{tombstones: []database.Book{{ID: "nofile"}}}
	res, err := SweepTombstones(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TombstonesCleaned != 1 || res.RefusedFiles != 0 || len(base.tombstones) != 0 {
		t.Errorf("result = %+v, tombstones left %d", res, len(base.tombstones))
	}
}
