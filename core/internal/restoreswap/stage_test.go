// file: core/internal/restoreswap/stage_test.go
// version: 1.0.0
// guid: e7576ce1-894f-4576-b593-1d60ae07a26f
// last-edited: 2026-11-07

package restoreswap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// stagerEnv makes the test binary a stager child of TestSwapSlotExclusive.
const stagerEnv = "RESTORESWAP_STAGER"

// osHost is a state directory and a store path on the real filesystem,
// where link(2)'s EEXIST is the kernel's, not a model's.
type osHost struct{ root, state, db string }

func newOSHost(root string) osHost {
	return osHost{root: root, state: filepath.Join(root, "state"), db: filepath.Join(root, "db", "main.pebble")}
}

func (h osHost) swapper(t testing.TB) *Swapper {
	t.Helper()
	s, err := New(Options{StateDir: h.state, BuildID: testBuild, Clock: testNow, Owners: []storage.OwnerSpec{wayne(1)}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// kinds are the stagers' kinds, cycled: every pairing of a restore and the
// two resets meets in one round.
var kinds = []string{"archive", "store", "revert", "factory", "checkpoint"}

// intent is stager n's intent of kind; a restore's staged item is made
// first, as its stager would have extracted it.
func (h osHost) intent(t testing.TB, n int, kind string) restorejournal.Intent {
	t.Helper()
	id := fmt.Sprintf("stager-%d", n)
	in := restorejournal.Intent{ID: id, DBPath: h.db, StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour),
		RequestedBy: "alfred", RequestedAt: testNow()}
	switch kind {
	case "store", "factory":
		in.Kind, in.Reset = restorejournal.KindReset, restorejournal.ResetKind(kind)
		for _, p := range []string{h.db, h.db + restorejournal.LevelsSuffix} {
			in.Targets = append(in.Targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(h.state, h.db, id, p)})
		}
	default:
		in.Kind = restorejournal.Kind(kind)
		staged := filepath.Join(restorejournal.StagingDir(h.db, id), "main.pebble")
		if err := os.MkdirAll(staged, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staged, "CURRENT"), []byte("MANIFEST-000001\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		in.Targets = []restorejournal.Target{{From: staged, To: h.db}}
	}
	return in
}

// slot reads what the one slot holds after a round: the intent in
// pending.json and every entry of restore/.
func (h osHost) slot(t testing.TB) (string, []string) {
	t.Helper()
	in, err := readIntent(vfs.Default, restorejournal.PendingPath(h.state))
	if err != nil {
		t.Fatalf("pending.json: %v", err)
	}
	ents, err := os.ReadDir(restorejournal.Dir(h.state))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return in.ID, names
}

// checkOneWinner checks a round: exactly one stager staged, every other got
// ErrSwapPending, and the slot holds the winner's intent and journal only.
func checkOneWinner(t testing.TB, h osHost, ids []string, errs []error) {
	t.Helper()
	winner := ""
	for i, err := range errs {
		switch {
		case err == nil && winner != "":
			t.Fatalf("two stagers staged: %s and %s", winner, ids[i])
		case err == nil:
			winner = ids[i]
		case !errors.Is(err, storage.ErrSwapPending):
			t.Fatalf("stager %s: %v, want ErrSwapPending", ids[i], err)
		}
	}
	if winner == "" {
		t.Fatal("no stager staged")
	}
	got, names := h.slot(t)
	if got != winner {
		t.Fatalf("pending.json holds %s, the winner is %s", got, winner)
	}
	if len(names) != 2 || !strings.Contains(strings.Join(names, " "), winner) {
		t.Fatalf("restore/ holds %v, want pending.json and %s/ only", names, winner)
	}
	recs, err := ReadJournal(vfs.Default, h.state, winner)
	if err != nil || len(recs) != 1 || recs[0].Step != StepStaged {
		t.Fatalf("the winner's journal: %v %v", steps(recs), err)
	}
}

// TestSwapSlotExclusive (06 §6.10; R8-1, SIMP8-1): concurrent stagers of
// any kinds, in one process and across processes, never both succeed:
// exactly one intent is written and every other stager gets
// storage.ErrSwapPending, decided by link(2) alone, with no mutex in the
// swap's code. A pending intent leaves the rollback guard's probe passing;
// only an in-flight one refuses.
func TestSwapSlotExclusive(t *testing.T) {
	if spec := os.Getenv(stagerEnv); spec != "" {
		os.Exit(stagerChild(t, spec))
	}
	t.Run("goroutines", func(t *testing.T) {
		for round := range 25 {
			h := newOSHost(t.TempDir())
			const n = 6
			ins := make([]restorejournal.Intent, n)
			ids := make([]string, n)
			for i := range n {
				ins[i] = h.intent(t, i, kinds[(round+i)%len(kinds)])
				ids[i] = ins[i].ID
			}
			s := h.swapper(t)
			errs := make([]error, n)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range n {
				wg.Go(func() {
					<-start
					errs[i] = s.Stage(context.Background(), ins[i])
				})
			}
			close(start)
			wg.Wait()
			checkOneWinner(t, h, ids, errs)
		}
	})
	t.Run("processes", func(t *testing.T) {
		if testing.Short() {
			t.Skip("re-executes the test binary")
		}
		for round := range 3 {
			h := newOSHost(t.TempDir())
			const n = 4
			ids := make([]string, n)
			cmds := make([]*exec.Cmd, n)
			for i := range n {
				kind := kinds[(round+i)%len(kinds)]
				ids[i] = h.intent(t, i, kind).ID
				cmds[i] = exec.Command(os.Args[0], "-test.run=^TestSwapSlotExclusive$", "-test.count=1")
				cmds[i].Env = append(os.Environ(), fmt.Sprintf("%s=%s|%d|%s", stagerEnv, h.root, i, kind))
				if err := cmds[i].Start(); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(h.root, "go"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			errs := make([]error, n)
			for i, c := range cmds {
				err := c.Wait()
				var exit *exec.ExitError
				switch {
				case err == nil:
				case errors.As(err, &exit) && exit.ExitCode() == childPending:
					errs[i] = storage.ErrSwapPending
				default:
					errs[i] = fmt.Errorf("stager process %d: %w", i, err)
				}
			}
			checkOneWinner(t, h, ids, errs)
		}
	})
	t.Run("no-mutex", func(t *testing.T) {
		lock := regexp.MustCompile(`sync\.(RW)?Mutex|syscall\.Flock|unix\.Flock|\.Lock\(\)`)
		for _, dir := range []string{".", filepath.Join("..", "..", "storage", "restorejournal")} {
			files, err := filepath.Glob(filepath.Join(dir, "*.go"))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if strings.HasSuffix(f, "_test.go") {
					continue
				}
				b, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				if loc := lock.FindIndex(b); loc != nil {
					t.Errorf("%s takes a lock (%q): the slot is decided by link(2) alone", f, b[loc[0]:loc[1]])
				}
			}
		}
	})
	t.Run("probe-refuses-only-in-flight", func(t *testing.T) {
		f := resetFixture(t, restorejournal.ResetStore, "inside")
		if _, ok, err := InFlight(f.mem, stateDir); err != nil || ok {
			t.Fatalf("a pending intent: in flight %v %v; the guard must pass it", ok, err)
		}
		out, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(context.Background())
		if err != nil || out.Boot != BootReset {
			t.Fatalf("P0a: %+v %v", out, err)
		}
		if id, ok, err := InFlight(f.mem, stateDir); err != nil || !ok || id != f.in.ID {
			t.Fatalf("a reset at set_aside: in flight %q %v %v; the guard must refuse it", id, ok, err)
		}
		other := f.in
		other.ID, other.Targets = "stager-late", nil
		for _, tg := range f.in.Targets {
			other.Targets = append(other.Targets, restorejournal.Target{From: tg.From, To: restorejournal.SetAsideDest(stateDir, other.DBPath, other.ID, tg.From)})
		}
		if err := newSwapper(t, f.mem, testBuild, testNow).Stage(context.Background(), other); !errors.Is(err, storage.ErrSwapPending) {
			t.Fatalf("staging while a swap is in flight: %v, want ErrSwapPending", err)
		}
	})
}

// childPending is a stager child's exit code for ErrSwapPending.
const childPending = 3

// stagerChild is one stager process: it waits for the parent's start file,
// stages its intent, and exits 0 (staged), childPending, or 1.
func stagerChild(t *testing.T, spec string) int {
	parts := strings.Split(spec, "|")
	if len(parts) != 3 {
		fmt.Fprintln(os.Stderr, "bad stager spec", spec)
		return 1
	}
	h := newOSHost(parts[0])
	var n int
	if _, err := fmt.Sscan(parts[1], &n); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	in := h.intent(t, n, parts[2])
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.root, "go")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "no start file")
			return 1
		}
		time.Sleep(time.Millisecond)
	}
	switch err := h.swapper(t).Stage(context.Background(), in); {
	case err == nil:
		return 0
	case errors.Is(err, storage.ErrSwapPending):
		return childPending
	default:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
}

// TestStageRefusals: every refusal of Stage stages nothing: no pending.json
// and no journal directory are left behind.
func TestStageRefusals(t *testing.T) {
	ctx := context.Background()
	nothingStaged := func(t *testing.T, fs vfs.FS, id string) {
		t.Helper()
		if ok, _ := exists(fs, restorejournal.JournalDir(stateDir, id)); ok {
			t.Fatalf("restore/%s was left", id)
		}
	}
	t.Run("slot-taken", func(t *testing.T) {
		f := restoreFixture(t, restorejournal.KindArchive, "inside", false)
		second := f.in
		second.ID = "r-second"
		if err := newSwapper(t, f.mem, testBuild, testNow).Stage(ctx, second); !errors.Is(err, storage.ErrSwapPending) {
			t.Fatalf("%v, want ErrSwapPending", err)
		}
		nothingStaged(t, f.mem, second.ID)
	})
	t.Run("staged-item-missing", func(t *testing.T) {
		mem := vfs.NewMem()
		in := restorejournal.Intent{ID: "r-gone", Kind: restorejournal.KindArchive, DBPath: dbPathOf("inside"),
			Targets: []restorejournal.Target{{From: "/batcave/staging/gone", To: dbPathOf("inside")}}, StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour)}
		if err := newSwapper(t, mem, testBuild, testNow).Stage(ctx, in); err == nil {
			t.Fatal("staged a restore whose staged item does not exist")
		}
		nothingStaged(t, mem, in.ID)
	})
	t.Run("reset-target-off-rule", func(t *testing.T) {
		mem := vfs.NewMem()
		db := dbPathOf("outside")
		in := restorejournal.Intent{ID: "reset-off", Kind: restorejournal.KindReset, Reset: restorejournal.ResetStore, DBPath: db,
			Targets: []restorejournal.Target{{From: db, To: "/joker/elsewhere/main.pebble"}}, StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour)}
		if err := newSwapper(t, mem, testBuild, testNow).Stage(ctx, in); err == nil {
			t.Fatal("staged a reset whose set-aside leaves the one rule")
		}
		nothingStaged(t, mem, in.ID)
	})
	t.Run("rename-refused", func(t *testing.T) {
		h := newOSHost(t.TempDir())
		if err := os.MkdirAll(h.db, 0o700); err != nil {
			t.Fatal(err)
		}
		in := h.intent(t, 0, "archive")
		parent := filepath.Dir(h.db)
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		s, err := New(Options{StateDir: h.state, BuildID: testBuild, Clock: testNow, Probe: restorejournal.OSProbe()})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Stage(ctx, in); !errors.Is(err, storage.ErrRenameRefused) {
			t.Fatalf("%v, want ErrRenameRefused", err)
		}
		if ok, _ := exists(vfs.Default, restorejournal.PendingPath(h.state)); ok {
			t.Fatal("a refused intent was written")
		}
	})
	t.Run("id-reused", func(t *testing.T) {
		f := restoreFixture(t, restorejournal.KindRevert, "outside", false)
		s := newSwapper(t, f.mem, testBuild, testNow)
		if _, err := fakeBoot(ctx, s, bootSucceeds); err != nil {
			t.Fatal(err)
		}
		again := f.in
		again.Targets = []restorejournal.Target{{From: restorejournal.SetAsideDest(stateDir, f.in.DBPath, f.in.ID, f.in.DBPath), To: f.in.DBPath}}
		if err := s.Stage(ctx, again); err == nil || errors.Is(err, storage.ErrSwapPending) {
			t.Fatalf("re-staging a used id: %v, want a refusal", err)
		}
		if ok, _ := exists(f.mem, restorejournal.PendingPath(stateDir)); ok {
			t.Fatal("a reused id was staged")
		}
	})
}
