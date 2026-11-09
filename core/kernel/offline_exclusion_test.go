// file: core/kernel/offline_exclusion_test.go
// version: 1.0.0
// guid: 6db49d0a-53e9-4c7e-86f2-51d39945ba37
// last-edited: 2026-11-09

package kernel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The swap lock's tests (R1-25 review B1): every opener of the live store
// holds storage's swap lock shared, and the offline restore, its escape and
// the offline reset hold it exclusively from before their stopped-service
// check until they return, so a service that starts while one of them runs
// is refused at P0a, and one that runs refuses them.

// holdStore holds the live store the way only an opener without the swap
// lock can: a kernel booted without a state directory runs no P0a, so it
// takes no swap lock, and refuseHeldStore's Pebble LOCK check is what sees
// it.
func (h *swapHost) holdStore(migs ...storage.Migration) *Kernel {
	h.t.Helper()
	c := h.config(nil, migs...)
	c.StateDir = ""
	return h.mustBoot(c)
}

// pauser blocks the nth Enter of label until release is closed.
type pauser struct {
	label   string
	nth     int
	mu      sync.Mutex
	seen    int
	reached chan struct{}
	release chan struct{}
}

func newPauser(label string, nth int) *pauser {
	return &pauser{label: label, nth: nth, reached: make(chan struct{}), release: make(chan struct{})}
}

func (p *pauser) Enter(label string) func() {
	if label == p.label {
		p.mu.Lock()
		n := p.seen
		p.seen++
		p.mu.Unlock()
		if n == p.nth {
			close(p.reached)
			<-p.release
		}
	}
	return func() {}
}

// The child half of TestOfflineRestoreRefusesAStartingService: boot the
// host's store as a service would and hold it until stdin says go, then
// write and close. A boot error is printed and the child exits 3.
const exclusionChildEnv = "MEDIACORE_R125_EXCLUSION_CHILD"

func exclusionChild(t *testing.T) int {
	state, db, _ := strings.Cut(os.Getenv(exclusionChildEnv), "|")
	h := &swapHost{t: t, state: state, db: db,
		ctx: bootseam.With(context.Background(), &bootseam.Seams{BuildID: swapBuild})}
	var deps module.Deps
	k, err := New(h.ctx, h.config(&deps))
	if err != nil {
		fmt.Println("BOOTERR", err)
		return 3
	}
	fmt.Println("READY")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	if err := deps.Storage.Keyspace.Set(context.Background(), []byte("cave"), []byte("written-by-service")); err != nil {
		fmt.Println("SETERR", err)
	}
	if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
		fmt.Println("CLOSEERR", err)
	}
	fmt.Println("CLOSED")
	return 0
}

// TestOfflineRestoreRefusesAStartingService (R1-25 review B1, probe 2): a
// service process started while restore-offline --archive extracts its
// archive, after the stopped-service check, is refused at P0a with the
// swap lock held, and the restore completes: the live store is the
// archive's and nothing the service could have written is lost. Before the
// lock the child booted the live store, the swap renamed it out from under
// the child, and the child's write landed in the set-aside copy.
func TestOfflineRestoreRefusesAStartingService(t *testing.T) {
	if os.Getenv(exclusionChildEnv) != "" {
		os.Exit(exclusionChild(t))
	}
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil))) // cave "1"
	saved := filepath.Join(t.TempDir(), "archived.pebble")
	copyTree(t, h.db, saved)
	h.close(h.mustBoot(h.config(nil, caveStep(2, "bane"))))
	var child *exec.Cmd
	var stdin io.WriteCloser
	var booted string
	c := h.config(nil)
	c.ArchiveExtract = func(_ context.Context, archive, dir string) (string, error) {
		copyTree(t, saved, dir)
		// The service manager (or an operator) starts the service while
		// the archive extracts.
		child = exec.Command(os.Args[0], "-test.run=^TestOfflineRestoreRefusesAStartingService$", "-test.count=1")
		child.Env = append(os.Environ(), exclusionChildEnv+"="+h.state+"|"+h.db)
		var err error
		if stdin, err = child.StdinPipe(); err != nil {
			return "", err
		}
		out, err := child.StdoutPipe()
		if err != nil {
			return "", err
		}
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			return "", err
		}
		r := bufio.NewReader(out)
		for booted == "" {
			line, err := r.ReadString('\n')
			if err != nil {
				return "", fmt.Errorf("the service child said nothing: %w", err)
			}
			if strings.HasPrefix(line, "BOOTERR") || strings.HasPrefix(line, "READY") {
				booted = strings.TrimSpace(line)
			}
		}
		return "archive-" + filepath.Base(archive), nil
	}
	res, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz"), AcceptLoss: true})
	if child == nil {
		t.Fatalf("the archive was never extracted: %v", err)
	}
	_, _ = io.WriteString(stdin, "go\n")
	werr := child.Wait()
	if !strings.HasPrefix(booted, "BOOTERR") || !strings.Contains(booted, storage.ErrSwapLockHeld.Error()) {
		t.Fatalf("the service started during the restore said %q (exit %v); want it refused at P0a with the swap lock held", booted, werr)
	}
	if err != nil {
		t.Fatalf("restore = %v, want it to complete", err)
	}
	if got := stepsOf(h.journal(res.ID)); got[len(got)-1] != "applied_offline" {
		t.Fatalf("journal %v, want applied_offline", got)
	}
	if got := h.readCave(); got != "1" {
		t.Fatalf("cave after the restore = %q, want the archive's 1", got)
	}
}

// TestBootRefusedDuringOfflineRestore (R1-25 review B1, probe 3): a boot
// started while an offline restore is between its renames, paused before
// the sidecar rewrite, is refused at P0a before it reads the slot, so it
// never resumes the in-flight intent or opens the half-swapped store; the
// restore then finishes and the next boot serves the checkpoint. Before
// the lock the boot resumed the same intent and opened the store, the
// restore's sidecar open failed against it, and step x moved the store the
// boot was serving into failed/.
func TestBootRefusedDuringOfflineRestore(t *testing.T) {
	h := newSwapHost(t)
	name := h.completedUpgrade() // live cave "bane" at v2; checkpoint at v1, cave "1"
	pause := newPauser("swap.sidecars", 0)
	offCtx := bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Labels: pause})
	var offErr error
	var res RestoreOfflineResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, offErr = RestoreOffline(offCtx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
	}()
	select {
	case <-pause.reached:
	case <-done:
		t.Fatalf("the offline restore finished without reaching its sidecar rewrite: %v", offErr)
	}
	before := h.slot()
	_, bootErr := New(h.ctx, h.keyed(h.config(nil, caveStep(2, "bane"))))
	after := h.slot()
	close(pause.release)
	<-done
	if !errors.Is(bootErr, storage.ErrSwapLockHeld) {
		t.Fatalf("a boot during the offline restore = %v, want it refused with the swap lock held", bootErr)
	}
	if len(before.InFlight) != 1 || len(after.InFlight) != 1 || before.InFlight[0] != after.InFlight[0] {
		t.Fatalf("slot before the boot %+v, after it %+v: the refused boot changed it", before, after)
	}
	if offErr != nil {
		t.Fatalf("the offline restore = %v, want it to complete", offErr)
	}
	if got := stepsOf(h.journal(res.ID)); got[len(got)-1] != "applied_offline" {
		t.Fatalf("journal %v, want applied_offline", got)
	}
	if got := h.readCave(); got != "1" {
		t.Fatalf("cave after the restore = %q, want the checkpoint's 1", got)
	}
}

// TestOfflineSwapsRefuseARunningService (R1-25 review B1): while an opener
// holds the swap lock shared (a booted service, or a service between its
// take and P0a), restore-offline in place from an archive or a checkpoint,
// the escape of a failed checkpoint restore and the offline reset each
// refuse with ErrServiceRunning before they read the slot, extract, copy,
// run BeforeSwap or stage anything. --into needs no stopped service
// (TestRestoreOfflineIntoEmptyPath).
func TestOfflineSwapsRefuseARunningService(t *testing.T) {
	type action func(t *testing.T, h *swapHost, c Config) error
	archive := func(t *testing.T, h *swapHost, c Config) error {
		c.ArchiveExtract = func(context.Context, string, string) (string, error) {
			t.Error("the archive was extracted under a running service")
			return "", errors.New("unreachable")
		}
		_, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz"), AcceptLoss: true})
		return err
	}
	checkpoint := func(t *testing.T, h *swapHost, c Config) error {
		_, err := RestoreOffline(h.ctx, h.keyed(c), RestoreOfflineRequest{Checkpoint: h.pointer(), AcceptLoss: true})
		return err
	}
	reset := func(t *testing.T, h *swapHost, c Config) error {
		_, err := StageResetOffline(h.ctx, c, "alfred", storage.ResetRequest{Kind: restorejournal.ResetStore, Confirm: storage.ResetConfirm})
		return err
	}
	for _, tc := range []struct {
		name   string
		act    action
		holder string // "boot": a booted service; "lock": the shared lock alone
	}{
		{"archive under a booted service", archive, "boot"},
		{"checkpoint under a booted service", checkpoint, "boot"},
		{"reset under a booted service", reset, "boot"},
		{"archive under a starting service", archive, "lock"},
		{"checkpoint under a starting service", checkpoint, "lock"},
		{"reset under a starting service", reset, "lock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSwapHost(t)
			h.completedUpgrade()
			c := h.config(nil)
			c.BeforeSwap = func(string) error { t.Error("BeforeSwap ran under a running service"); return nil }
			switch tc.holder {
			case "boot":
				k := h.mustBoot(h.config(nil, caveStep(2, "bane")))
				defer h.close(k)
			case "lock":
				l, err := storage.LockSwapShared(vfs.Default, h.state)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
			}
			err := tc.act(t, h, c)
			if !errors.Is(err, ErrServiceRunning) || !errors.Is(err, storage.ErrSwapLockHeld) {
				t.Fatalf("%s = %v, want ErrServiceRunning wrapping storage.ErrSwapLockHeld", tc.name, err)
			}
			h.assertNothingStaged()
		})
	}
	t.Run("escape under a starting service", func(t *testing.T) {
		h := newSwapHost(t)
		h.close(h.mustBoot(h.config(nil)))
		const id = "r-checkpoint"
		h.stage(id, restorejournal.KindCheckpoint, h.stagedCopy(id, "robin"))
		if _, err := h.boot(h.config(nil, brokenStep)); !errors.Is(err, ErrCheckpointRestoreFailedHere) {
			t.Fatalf("restore boot with a failing step = %v", err)
		}
		l, err := storage.LockSwapShared(vfs.Default, h.state)
		if err != nil {
			t.Fatal(err)
		}
		prev := bootseam.With(t.Context(), &bootseam.Seams{BuildID: "batcomputer-r0"})
		_, err = RestoreOffline(prev, h.config(nil), RestoreOfflineRequest{Checkpoint: h.pointer()})
		if cerr := l.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if !errors.Is(err, ErrServiceRunning) {
			t.Fatalf("escape = %v, want ErrServiceRunning", err)
		}
		if _, err := os.Stat(filepath.Join(restorejournal.JournalDir(h.state, id), "escape.started")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the refused escape marked its start: %v", err)
		}
		if got := h.journal(id); got[len(got)-1].Step != "failed" {
			t.Fatalf("journal %v, want it still at failed", stepsOf(got))
		}
		if _, err := RestoreOffline(prev, h.config(nil), RestoreOfflineRequest{Checkpoint: h.pointer()}); err != nil {
			t.Fatalf("the escape once the service let go = %v", err)
		}
	})
}

// TestOpenersHoldTheSwapLock (R1-25 review B1): a booted kernel holds the
// swap lock shared until it closes, and so does an offline open until
// Close, so an exclusive take (an offline swap) is refused while either is
// open and succeeds once it closed. A boot that fails after P0a releases
// it too.
func TestOpenersHoldTheSwapLock(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	exclusive := func() error {
		l, err := storage.LockSwapExclusive(vfs.Default, h.state)
		if err != nil {
			return err
		}
		return l.Close()
	}
	k := h.mustBoot(h.config(nil))
	if err := exclusive(); !errors.Is(err, storage.ErrSwapLockHeld) {
		t.Fatalf("an exclusive take under a booted kernel = %v, want ErrSwapLockHeld", err)
	}
	h.close(k)
	if err := exclusive(); err != nil {
		t.Fatalf("an exclusive take once the kernel closed = %v", err)
	}

	o, err := OpenOffline(h.ctx, h.config(nil), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := exclusive(); !errors.Is(err, storage.ErrSwapLockHeld) {
		t.Fatalf("an exclusive take under an offline open = %v, want ErrSwapLockHeld", err)
	}
	if _, err := StageResetOffline(h.ctx, h.config(nil), "alfred",
		storage.ResetRequest{Kind: restorejournal.ResetStore, Confirm: storage.ResetConfirm}); !errors.Is(err, ErrServiceRunning) {
		t.Fatalf("an offline reset under an offline open = %v, want ErrServiceRunning", err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if err := exclusive(); err != nil {
		t.Fatalf("an exclusive take once the offline open closed = %v", err)
	}

	if _, err := h.boot(h.config(nil, brokenStep)); err == nil {
		t.Fatal("the failing upgrade booted")
	}
	if err := exclusive(); err != nil {
		t.Fatalf("an exclusive take after a failed boot = %v; the failed boot kept the swap lock", err)
	}

	l, err := storage.LockSwapExclusive(vfs.Default, h.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOffline(h.ctx, h.config(nil), true); !errors.Is(err, storage.ErrSwapLockHeld) {
		t.Fatalf("an offline open under an offline swap = %v, want ErrSwapLockHeld", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestP0aTakesTheSwapLockAlone (R1-25 review B3): a serve boot holds the
// swap lock exclusively through P0a and shared after it. A booted service
// lets a shared opener in; a second serve boot beside it, and a boot beside
// any shared holder (an offline or standalone open, an op-runner child),
// is refused at P0a and leaves the swap the running service staged where
// it is (before the exclusive P0a, the second boot applied it under the
// first); a boot under an offline swap's exclusive hold says so. Once
// nothing else holds the lock, the next boot applies the staged swap.
func TestP0aTakesTheSwapLockAlone(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil))) // cave "1"
	staged := h.stagedCopy("r-archive", "robin")
	first := h.mustBoot(h.config(nil))
	// A live restore stages while the service runs, then exits 75.
	h.stage("r-archive", restorejournal.KindArchive, staged)

	shared, err := storage.LockSwapShared(vfs.Default, h.state)
	if err != nil {
		t.Fatalf("a shared take beside a booted service = %v; the boot did not turn its hold shared", err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	assertStaged := func(when string) {
		t.Helper()
		if _, err := os.Stat(restorejournal.PendingPath(h.state)); err != nil {
			t.Fatalf("%s: the staged intent left the slot: %v", when, err)
		}
		if _, ok, err := restoreswap.InFlight(vfs.Default, h.state); err != nil || ok {
			t.Fatalf("%s: in flight = %v, %v; want nothing started", when, ok, err)
		}
		if _, err := os.Stat(staged); err != nil {
			t.Fatalf("%s: the staged store left staging: %v", when, err)
		}
	}
	const opener = "another process has the store open"
	if _, err := h.boot(h.config(nil)); !errors.Is(err, storage.ErrSwapLockHeld) || !strings.Contains(err.Error(), opener) {
		t.Fatalf("a second serve boot beside the first = %v, want it refused at P0a naming another opener", err)
	}
	assertStaged("after a second serve boot")
	h.close(first)

	holder, err := storage.LockSwapShared(vfs.Default, h.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.boot(h.config(nil)); !errors.Is(err, storage.ErrSwapLockHeld) || !strings.Contains(err.Error(), opener) {
		t.Fatalf("a boot beside a shared holder = %v, want it refused at P0a naming another opener", err)
	}
	assertStaged("after a boot beside a shared holder")
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}

	swap, err := storage.LockSwapExclusive(vfs.Default, h.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.boot(h.config(nil)); !errors.Is(err, storage.ErrSwapLockHeld) || !strings.Contains(err.Error(), "restore-offline or storage reset is running") {
		t.Fatalf("a boot under an offline swap's hold = %v, want it refused naming the offline swap", err)
	}
	assertStaged("after a boot under an offline swap")
	if err := swap.Close(); err != nil {
		t.Fatal(err)
	}

	// The boot that applies it holds the lock alone through P0a: its step b
	// re-checks the renames through the probe (a bootseam seam), and a
	// shared take from there is refused.
	var during []error
	h.ctx = bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild,
		Probe: sharedTakeProbe{Probe: restorejournal.OSProbe(), state: h.state, during: &during}})
	if got := h.readCave(); got != "robin" {
		t.Fatalf("cave once nothing else held the lock = %q, want the staged robin", got)
	}
	if len(during) == 0 || !errors.Is(during[0], storage.ErrSwapLockHeld) {
		t.Fatalf("shared takes from inside P0a = %v, want the first refused: P0a holds the lock exclusively", during)
	}
}

// sharedTakeProbe is a probe that takes the swap lock shared, and releases
// it, at each ItemDev call, recording what the take returned: the boot's
// hold as its swap runs.
type sharedTakeProbe struct {
	restorejournal.Probe
	state  string
	during *[]error
}

func (p sharedTakeProbe) ItemDev(path string) (uint64, error) {
	l, err := storage.LockSwapShared(vfs.Default, p.state)
	if err == nil {
		err = l.Close()
	}
	*p.during = append(*p.during, err)
	return p.Probe.ItemDev(path)
}

// TestRestoreOfflineIntoBesideARunningService (R1-25 review B1, for
// R1-27): --into copies a source into the scratch area and never touches
// the live store, so it takes no swap lock and runs while a serve boot
// holds the lock shared. R1-27's make rollback-restore runs
// restore-offline --archive --into beside a serving service to read the
// archive's storage levels. The copy leaves the service's hold as it was
// (an exclusive take is still refused) and stages nothing.
// TestRestoreOfflineIntoEmptyPath runs --checkpoint --into the same way.
func TestRestoreOfflineIntoBesideARunningService(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil))) // cave "1"
	saved := filepath.Join(t.TempDir(), "archived.pebble")
	copyTree(t, h.db, saved)
	held := h.mustBoot(h.config(nil, caveStep(2, "bane")))
	defer h.close(held)

	into := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints", "scratch", "levels")
	c := h.config(nil)
	c.BeforeSwap = func(string) error { t.Error("BeforeSwap ran for an --into copy"); return nil }
	c.ArchiveExtract = func(_ context.Context, archive, dir string) (string, error) {
		copyTree(t, saved, dir)
		return "archive-" + filepath.Base(archive), nil
	}
	res, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz"), Into: into})
	if err != nil {
		t.Fatalf("--archive --into beside a running service = %v, want the copy", err)
	}
	if res.Into != into {
		t.Fatalf("result %+v, want the copy at %s", res, into)
	}
	if entries, err := os.ReadDir(into); err != nil || len(entries) == 0 {
		t.Fatalf("the copy at %s: %d entries (%v), want the archive's store", into, len(entries), err)
	}
	if l, err := storage.LockSwapExclusive(vfs.Default, h.state); !errors.Is(err, storage.ErrSwapLockHeld) {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("an exclusive take after the copy = %v, want the running service still holding the swap lock", err)
	}
	h.assertNothingStaged()
}
