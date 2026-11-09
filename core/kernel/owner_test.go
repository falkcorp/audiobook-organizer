// file: core/kernel/owner_test.go
// version: 1.0.1
// guid: 63dcc686-2f07-4d9b-bca2-c14f469f79c9
// last-edited: 2026-11-09

package kernel

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// treeState is every path under root with its type, mode, size and
// modification time: a write anywhere under it, a directory entry added or
// removed included, changes it.
func treeState(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%v %d %d", fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOfflineWritesRefuseANonOwner (R1-25 review B-1): storage
// restore-offline in place and --into (--archive and --checkpoint each),
// storage reset and a read-write offline open refuse a process that is not
// the state directory's owner (root, as euid 0, or a stranger), with
// ErrNotStateDirOwner naming the sudo -u fix, before they build a kernel,
// take a lock or read the slot: the refusal is the owner's even while a
// service holds the swap lock (a check after the lock would answer
// ErrServiceRunning; --into, which takes no swap lock, would succeed), and
// the state directory, the store and the checkpoint directory are as they
// were, to the modification time. A read-only open still runs as either.
func TestOfflineWritesRefuseANonOwner(t *testing.T) {
	h := newSwapHost(t)
	h.completedUpgrade()
	saved := filepath.Join(t.TempDir(), "archived.pebble")
	copyTree(t, h.db, saved)
	config := func() Config {
		c := h.keyed(h.config(nil, caveStep(2, "bane")))
		c.BeforeSwap = func(string) error { t.Error("BeforeSwap ran for a refused non-owner"); return nil }
		c.ArchiveExtract = func(_ context.Context, archive, dir string) (string, error) {
			copyTree(t, saved, dir)
			return "archive-" + filepath.Base(archive), nil
		}
		return c
	}
	archive := filepath.Join(t.TempDir(), "joker.tar.gz")
	tree := filepath.Dir(h.state)
	into := func(who string) string {
		return filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints", "scratch", "copy-"+strings.ReplaceAll(who, " ", "-"))
	}

	for _, who := range []struct {
		name string
		euid int
	}{{"root", 0}, {"a stranger", os.Geteuid() + 1}} {
		euid := who.euid
		ctx := bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Euid: func() int { return euid }})
		for _, tc := range []struct {
			name string
			act  func() error
		}{
			{"restore-offline --archive in place", func() error {
				_, err := RestoreOffline(ctx, config(), RestoreOfflineRequest{Archive: archive, AcceptLoss: true})
				return err
			}},
			{"restore-offline --checkpoint in place", func() error {
				_, err := RestoreOffline(ctx, config(), RestoreOfflineRequest{Checkpoint: h.pointer(), AcceptLoss: true})
				return err
			}},
			// --into takes no swap lock, so without the check it would
			// succeed beside the lock held below, and leave its copy.
			{"restore-offline --archive --into", func() error {
				_, err := RestoreOffline(ctx, config(), RestoreOfflineRequest{Archive: archive, Into: into(who.name)})
				return err
			}},
			{"restore-offline --checkpoint --into", func() error {
				_, err := RestoreOffline(ctx, config(), RestoreOfflineRequest{Checkpoint: h.pointer(), Into: into(who.name)})
				return err
			}},
			{"storage reset", func() error {
				_, err := StageResetOffline(ctx, config(), "alfred", storage.ResetRequest{Kind: restorejournal.ResetStore, Confirm: storage.ResetConfirm})
				return err
			}},
			{"read-write offline open", func() error {
				o, err := OpenOffline(ctx, config(), false)
				if err == nil {
					err = errors.Join(errors.New("the open succeeded"), o.Close())
				}
				return err
			}},
		} {
			t.Run(who.name+"/"+tc.name, func(t *testing.T) {
				before := treeState(t, tree)
				service, err := storage.LockSwapShared(vfs.Default, h.state)
				if err != nil {
					t.Fatal(err)
				}
				err = tc.act()
				if cerr := service.Close(); cerr != nil {
					t.Fatal(cerr)
				}
				if !errors.Is(err, ErrNotStateDirOwner) || !strings.Contains(err.Error(), "sudo -u ") {
					t.Fatalf("%s as %s = %v, want ErrNotStateDirOwner naming sudo -u", tc.name, who.name, err)
				}
				after := treeState(t, tree)
				for p, was := range before {
					if now, ok := after[p]; !ok || now != was {
						t.Errorf("%s changed: %q -> %q", p, was, now)
					}
				}
				for p := range after {
					if _, ok := before[p]; !ok {
						t.Errorf("%s was created", p)
					}
				}
				h.assertNothingStaged()
			})
		}
		t.Run(who.name+"/a read-only open still runs", func(t *testing.T) {
			o, err := OpenOffline(ctx, config(), true)
			if err != nil {
				t.Fatalf("a read-only open as %s = %v, want it to run", who.name, err)
			}
			if err := o.Close(); err != nil {
				t.Fatal(err)
			}
			h.assertNothingStaged()
		})
	}
}

// failingDowngrade is a P0a hold whose Downgrade fails: the conversion
// flock(2) does not promise is atomic, lost to another process.
type failingDowngrade struct {
	bootseam.SwapLock
	err error
}

func (f failingDowngrade) Downgrade() error { return f.err }

// TestP0aFailedDowngradeStopsTheBoot (R1-25 review S-2): a serve boot whose
// P0a hold cannot be turned shared may hold nothing, so the boot stops
// there, as a crash would, and returns the Downgrade's error; the swap that
// P0a applied is left to the next boot, which finishes it.
func TestP0aFailedDowngradeStopsTheBoot(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil))) // cave "1"
	h.stage("r-archive", restorejournal.KindArchive, h.stagedCopy("r-archive", "robin"))

	lost := errors.New("joker took the lock between the drop and the shared take")
	ctx := bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild,
		SwapLock: func(l bootseam.SwapLock) bootseam.SwapLock { return failingDowngrade{SwapLock: l, err: lost} }})
	k, err := New(ctx, h.config(nil))
	if err == nil {
		h.close(k)
		t.Fatal("a boot whose Downgrade failed booted")
	}
	if !errors.Is(err, lost) {
		t.Fatalf("boot = %v, want the Downgrade's error", err)
	}
	if got := h.readCave(); got != "robin" {
		t.Fatalf("cave after the next boot = %q, want the staged robin", got)
	}
}
