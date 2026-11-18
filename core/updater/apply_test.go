// file: core/updater/apply_test.go
// version: 1.0.0
// guid: 6f1d3a85-0c27-4be9-a3d6-9e48b2c1f057
// last-edited: 2026-11-18

//go:build unix

package updater

// 05-updater 5U.11's apply rows: HEAD's raw-asset pipeline (download into
// <exe>.new, rename swap, .prev kept, restart) and its two additions, the
// max_download cap and the --version-json self-test.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage/restorejournal"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// applyRig is a rig with a newer release published and the check done.
func applyRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	r := newRig(t, "v0.223.0", src, opts...)
	if _, err := r.u.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestApply_RestartExits75(t *testing.T) {
	r := applyRig(t)

	if err := r.u.Apply(r.rc(KindApply), ""); err != nil {
		t.Fatal(err)
	}

	if got := r.rst.calls(); len(got) != 1 || got[0] != restartReasonApply {
		t.Fatalf("restarts = %v, want exactly one with reason %q", got, restartReasonApply)
	}
	if RestartExitCode != 75 {
		t.Fatalf("RestartExitCode = %d, want 75 (EX_TEMPFAIL, the committed unit's Restart=on-failure)", RestartExitCode)
	}
}

func TestApply_RestartFailureIsReportedAfterTheSwap(t *testing.T) {
	r := applyRig(t)
	r.rst.err = errors.New("the supervisor said no")

	err := r.u.Apply(r.rc(KindApply), "")
	if err == nil || !strings.Contains(err.Error(), "restart failed") {
		t.Fatalf("err = %v, want one naming the failed restart", err)
	}
	if got := readFile(t, r.exe); got != string(fakeBinary(testApp, "v0.224.0")) {
		t.Errorf("<exe> after a failed restart = %q, want the new release (the swap stands)", got)
	}
}

func TestApply_SwapAtomicAndPrevKept(t *testing.T) {
	t.Run("new binary in place, previous kept as .prev, older .prev replaced", func(t *testing.T) {
		r := applyRig(t)
		if err := os.WriteFile(r.exe+".prev", []byte("an older previous binary"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := r.u.Apply(r.rc(KindApply), ""); err != nil {
			t.Fatal(err)
		}

		if got := readFile(t, r.exe); got != string(fakeBinary(testApp, "v0.224.0")) {
			t.Errorf("<exe> = %q, want the new release", got)
		}
		if got := readFile(t, r.exe+".prev"); got != oldBinary {
			t.Errorf("<exe>.prev = %q, want the previous running binary", got)
		}
		assertAbsent(t, r.exe+".new")
		assertAbsent(t, r.exe+".old")
		fi, err := os.Stat(r.exe)
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("<exe> mode = %v (%v), want 0755", fi.Mode().Perm(), err)
		}
		if st := r.u.Status(); st.PreviousKept != "v0.223.0" {
			t.Errorf("PreviousKept = %q, want the replaced version", st.PreviousKept)
		}
	})

	t.Run("a failed second rename restores <exe> from .prev", func(t *testing.T) {
		r := applyRig(t)
		calls := 0
		r.u.rename = func(oldpath, newpath string) error {
			calls++
			if calls == 2 { // <exe>.new -> <exe>
				return errors.New("disk on fire")
			}
			return os.Rename(oldpath, newpath)
		}

		err := r.u.Apply(r.rc(KindApply), "")
		if err == nil || !strings.Contains(err.Error(), "rename new binary into place") {
			t.Fatalf("err = %v, want the second rename's failure", err)
		}
		r.assertUntouched()
	})

	t.Run("a failed first rename leaves <exe> alone and removes the download", func(t *testing.T) {
		r := applyRig(t)
		r.u.rename = func(string, string) error { return errors.New("disk on fire") }

		if err := r.u.Apply(r.rc(KindApply), ""); err == nil {
			t.Fatal("a failed rename was reported as success")
		}
		r.assertUntouched()
	})
}

// TestApply_RawAssetNoBackup: the pipeline is HEAD's. The one asset fetched is
// the raw executable; no archive and no checksums file are asked for, and the
// updater has no backup runner to call (Deps has none).
func TestApply_RawAssetNoBackup(t *testing.T) {
	r := applyRig(t)
	if err := r.u.Apply(r.rc(KindApply), ""); err != nil {
		t.Fatal(err)
	}
	want := RawBinary(testBinary)(Release{}, goos(), goarch())
	if got := r.src.openedNames(); len(got) != 1 || got[0] != want {
		t.Fatalf("assets opened = %v, want exactly [%s]", got, want)
	}
}

func TestApply_OversizeAborts(t *testing.T) {
	small := func() Settings { s := DefaultSettings(ModeCheck); s.MaxDownload = 64; return s }
	// Each case isolates one of the three places the cap is held: the size the
	// release lists, the size the transfer advertises, and the bytes that
	// arrive.
	rig := func(t *testing.T, listed, advertised int64) *rig {
		t.Helper()
		r := applyRig(t, withSettings(small))
		rel := r.src.rels["v0.224.0"]
		url := rel.Assets[0].URL
		rel.Assets[0].Size = listed
		r.src.rels["v0.224.0"] = rel
		r.src.sizes[url] = advertised
		r.src.bodies[url] = append(fakeBinary(testApp, "v0.224.0"), make([]byte, 200)...)
		if advertised >= 0 && advertised <= 64 {
			r.src.bodies[url] = r.src.bodies[url][:advertised]
		}
		return r
	}

	t.Run("a listed size over the cap is refused before the asset is opened", func(t *testing.T) {
		r := rig(t, 1<<20, -1)
		if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrOversize) {
			t.Fatalf("err = %v, want ErrOversize", err)
		}
		if got := r.src.openedNames(); len(got) != 0 {
			t.Errorf("opened %v: the listed size was not checked first", got)
		}
		r.assertUntouched()
	})

	t.Run("an advertised size over the cap is refused before a byte is read", func(t *testing.T) {
		r := rig(t, 0, 1<<20)
		if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrOversize) {
			t.Fatalf("err = %v, want ErrOversize", err)
		}
		r.assertUntouched()
		assertAbsent(t, r.exe+".new")
	})

	t.Run("a body that runs past the cap with no size anywhere is cut off", func(t *testing.T) {
		r := rig(t, 0, -1)
		if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrOversize) {
			t.Fatalf("err = %v, want ErrOversize", err)
		}
		r.assertUntouched()
	})

	t.Run("a body exactly at the cap is accepted", func(t *testing.T) {
		body := fakeBinary(testApp, "v0.224.0")
		exact := func() Settings { s := DefaultSettings(ModeCheck); s.MaxDownload = int64(len(body)); return s }
		r := applyRig(t, withSettings(exact))
		r.src.bodies[r.src.rels["v0.224.0"].Assets[0].URL] = body

		if err := r.u.Apply(r.rc(KindApply), ""); err != nil {
			t.Fatalf("a body of %d bytes under a %d-byte cap: %v", len(body), len(body), err)
		}
	})

	t.Run("one byte over the cap with no size anywhere is refused", func(t *testing.T) {
		body := fakeBinary(testApp, "v0.224.0")
		exact := func() Settings { s := DefaultSettings(ModeCheck); s.MaxDownload = int64(len(body)) - 1; return s }
		r := applyRig(t, withSettings(exact))
		rel := r.src.rels["v0.224.0"]
		rel.Assets[0].Size = 0
		r.src.rels["v0.224.0"] = rel
		r.src.sizes[rel.Assets[0].URL] = -1

		if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrOversize) {
			t.Fatalf("err = %v, want ErrOversize", err)
		}
		r.assertUntouched()
	})
}

func TestApply_FailedDownloadDeletesNew(t *testing.T) {
	t.Run("a transfer that fails half way", func(t *testing.T) {
		r := applyRig(t)
		r.src.midErr[r.src.rels["v0.224.0"].Assets[0].URL] = errors.New("connection reset")

		err := r.u.Apply(r.rc(KindApply), "")
		if !errors.Is(err, ErrDownloadFailed) {
			t.Fatalf("err = %v, want ErrDownloadFailed", err)
		}
		r.assertUntouched()
	})

	t.Run("a transfer that ends short of its advertised size", func(t *testing.T) {
		r := applyRig(t)
		url := r.src.rels["v0.224.0"].Assets[0].URL
		r.src.sizes[url] = int64(len(r.src.bodies[url])) + 10

		err := r.u.Apply(r.rc(KindApply), "")
		if !errors.Is(err, ErrDownloadFailed) {
			t.Fatalf("err = %v, want ErrDownloadFailed", err)
		}
		r.assertUntouched()
	})

	t.Run("a source that cannot open the asset", func(t *testing.T) {
		r := applyRig(t)
		r.src.openErr = errors.New("503")

		if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrDownloadFailed) {
			t.Fatalf("err = %v, want ErrDownloadFailed", err)
		}
		r.assertUntouched()
	})
}

func TestApply_SelfTestFailsKeepsOldBinary(t *testing.T) {
	cases := map[string][]byte{
		"another app":       fakeBinary("metropolis", "v0.224.0"),
		"another version":   fakeBinary(testApp, "v0.100.0"),
		"not a program":     []byte("this is not an executable"),
		"exits non-zero":    scriptBinary("exit 3"),
		"prints no JSON":    scriptBinary("echo hello"),
		"prints too little": scriptBinary(`echo '{"app":"gotham","version":"v0.224.0"}'`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			src := newFakeSource()
			src.add("v0.224.0", body, true)
			r := newRig(t, "v0.223.0", src)
			if _, err := r.u.Check(t.Context()); err != nil {
				t.Fatal(err)
			}

			err := r.u.Apply(r.rc(KindApply), "")
			if !errors.Is(err, ErrSelfTest) {
				t.Fatalf("err = %v, want ErrSelfTest", err)
			}
			r.assertUntouched()
		})
	}
}

// TestApply_LegacySkipSelfTest: the gin path keeps R0's behaviour until F8-04
// deletes it, so a release from before --version-json still applies there.
func TestApply_LegacySkipSelfTest(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", []byte("a binary that predates --version-json"), true)
	r := newRig(t, "v0.223.0", src, withLegacySelfTestOff())
	if _, err := r.u.Check(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := r.u.Apply(r.rc(KindApply), ""); err != nil {
		t.Fatalf("the legacy path refused a pre-self-test release: %v", err)
	}
	if got := readFile(t, r.exe); got != "a binary that predates --version-json" {
		t.Errorf("<exe> = %q, want the downloaded bytes", got)
	}
}

func TestApply_BinaryDirNotWritableRefusedAndDegraded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	r := applyRig(t)
	dir := filepath.Dir(r.exe)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := r.u.Apply(r.rc(KindApply), "")
	if !errors.Is(err, ErrBinaryNotWritable) {
		t.Fatalf("Apply = %v, want ErrBinaryNotWritable", err)
	}
	if got := r.src.openedNames(); len(got) != 0 {
		t.Errorf("an unwritable directory still downloaded %v", got)
	}
	if err := r.u.Rollback(r.rc(KindRollback)); err == nil {
		t.Error("Rollback in an unwritable directory did not refuse")
	}
	st, err := r.u.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.DisabledReason != "binary_not_writable" {
		t.Errorf("DisabledReason = %q, want binary_not_writable", st.DisabledReason)
	}
	if res := r.u.Probe(t.Context()); res.Reason != "binary_not_writable" {
		t.Errorf("probe reason = %q, want binary_not_writable", res.Reason)
	}
}

func TestApply_Refusals(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		r := applyRig(t, withMode(ModeOff))
		if err := r.u.Apply(r.rc(KindApply), "v0.224.0"); !errors.Is(err, ErrDisabled) {
			t.Fatalf("err = %v, want ErrDisabled", err)
		}
		r.assertUntouched()
	})
	t.Run("nothing offered", func(t *testing.T) {
		r := newRig(t, "v0.224.0", newFakeSource())
		if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrNoUpdate) {
			t.Fatalf("err = %v, want ErrNoUpdate", err)
		}
	})
	t.Run("not newer", func(t *testing.T) {
		r := applyRig(t)
		if err := r.u.Apply(r.rc(KindApply), "v0.222.0"); !errors.Is(err, ErrNotNewer) {
			t.Fatalf("err = %v, want ErrNotNewer", err)
		}
		r.assertUntouched()
	})
	t.Run("a version that is not a release tag", func(t *testing.T) {
		r := applyRig(t)
		for _, v := range []string{"../../etc/passwd", "v1.2.3/../x", "latest", "https://example.invalid/x", "v1.2"} {
			if err := r.u.Apply(r.rc(KindApply), v); !errors.Is(err, ErrInvalidVersion) {
				t.Errorf("Apply(%q) = %v, want ErrInvalidVersion", v, err)
			}
		}
		r.assertUntouched()
	})
	t.Run("a tag the source does not have", func(t *testing.T) {
		r := applyRig(t)
		if err := r.u.Apply(r.rc(KindApply), "v0.300.0"); !errors.Is(err, ErrReleaseNotFound) {
			t.Fatalf("err = %v, want ErrReleaseNotFound", err)
		}
		r.assertUntouched()
	})
	t.Run("a release with no asset for this platform is no_asset", func(t *testing.T) {
		r := applyRig(t)
		rel := r.src.rels["v0.224.0"]
		rel.Assets = nil
		r.src.rels["v0.224.0"] = rel
		if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrNoAsset) {
			t.Fatalf("err = %v, want ErrNoAsset", err)
		}
		r.assertUntouched()
	})
	t.Run("no restarter", func(t *testing.T) {
		r := applyRig(t)
		r.u.d.Restarter = nil
		if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrNoRestarter) {
			t.Fatalf("err = %v, want ErrNoRestarter", err)
		}
	})
	t.Run("a source without tag lookup serves only its latest release", func(t *testing.T) {
		r := applyRig(t, withoutTagAPI())
		if err := r.u.Apply(r.rc(KindApply), "v0.300.0"); !errors.Is(err, ErrReleaseNotFound) {
			t.Fatalf("a tag that is not the latest: err = %v, want ErrReleaseNotFound", err)
		}
		if err := r.u.Apply(r.rc(KindApply), "v0.224.0"); err != nil {
			t.Fatalf("the latest tag by name: %v", err)
		}
	})
}

// writeIntent puts an intent file where the swap slot reads it.
func writeIntent(t *testing.T, path, kind string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"20261108T000000Z-0a0b0c0d","kind":"` + kind + `"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApply_RefusedWhileResetPending(t *testing.T) {
	r := applyRig(t)
	writeIntent(t, restorejournal.PendingPath(r.state), "reset")

	err := r.u.Apply(r.rc(KindApply), "")
	if !errors.Is(err, restorejournal.ErrSwapPending) || !strings.Contains(err.Error(), restorejournal.TokenRestorePending) {
		t.Fatalf("err = %v, want ErrSwapPending carrying %s", err, restorejournal.TokenRestorePending)
	}
	if !strings.Contains(err.Error(), "reset") {
		t.Errorf("err = %v, want the message to name the kind", err)
	}
	if got := r.src.openedNames(); len(got) != 0 {
		t.Errorf("a pending swap still downloaded %v", got)
	}
	r.assertUntouched()
}

func TestApply_RefusedWhileOfflineRestoreInFlight(t *testing.T) {
	r := applyRig(t)
	writeIntent(t, filepath.Join(restorejournal.JournalDir(r.state, "20261108T000000Z-0a0b0c0d"), "inflight.json"), "checkpoint")

	err := r.u.Apply(r.rc(KindApply), "")
	if !errors.Is(err, restorejournal.ErrSwapPending) || !strings.Contains(err.Error(), restorejournal.TokenRestoreInflight) {
		t.Fatalf("err = %v, want ErrSwapPending carrying %s", err, restorejournal.TokenRestoreInflight)
	}
	r.assertUntouched()
}

func TestApply_SlotCannotBeReadRefuses(t *testing.T) {
	r := applyRig(t)
	// pending.json as a directory: it opens, and reading it fails.
	if err := os.MkdirAll(restorejournal.PendingPath(r.state), 0o700); err != nil {
		t.Fatal(err)
	}
	err := r.u.Apply(r.rc(KindApply), "")
	if err == nil || !strings.Contains(err.Error(), "cannot read the swap slot") {
		t.Fatalf("err = %v, want the unreadable-slot refusal", err)
	}
	r.assertUntouched()
}

func TestApply_NoStateDirRefuses(t *testing.T) {
	r := applyRig(t, withoutState())
	if err := r.u.Apply(r.rc(KindApply), ""); !errors.Is(err, ErrNoStateDir) {
		t.Fatalf("err = %v, want ErrNoStateDir", err)
	}
	r.assertUntouched()
}

// TestApply_SwapStagedDuringDownloadRefuses: a restore staged while the
// release downloads must still find the binary that staged it.
func TestApply_SwapStagedDuringDownloadRefuses(t *testing.T) {
	r := applyRig(t)
	staged := false
	r.u.source = stagingSource{Source: r.u.source, onOpen: func() {
		staged = true
		writeIntent(t, restorejournal.PendingPath(r.state), "reset")
	}}

	err := r.u.Apply(r.rc(KindApply), "")
	if !staged {
		t.Fatal("the release was never downloaded")
	}
	if !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("err = %v, want the re-check before the rename to refuse", err)
	}
	r.assertUntouched()
}

// TestApply_CancelledBeforeSwapKeepsOldBinary: a cancel that lands after the
// download and before the first rename stops the apply and leaves nothing.
func TestApply_CancelledBeforeSwapKeepsOldBinary(t *testing.T) {
	// The legacy path skips the self-test, so the cancel is first seen by the
	// check just before the rename (the self-test has its own: a cancelled
	// probe is a failed probe).
	r := applyRig(t, withLegacySelfTestOff())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.u.source = stagingSource{Source: r.u.source, onOpen: cancel}

	err := r.u.Apply(taskstest.NewFakeRun(ctx, KindApply).RC(), "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	r.assertUntouched()
}

// TestInstall_LegacyEntryHasNoRestart: the gin path's entry runs the same
// pipeline and leaves the restart to its caller; it needs no Restarter.
func TestInstall_LegacyEntryHasNoRestart(t *testing.T) {
	r := applyRig(t, withLegacySelfTestOff())
	r.u.d.Restarter = nil

	if err := r.u.Install(t.Context(), "v0.224.0"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, r.exe); got != string(fakeBinary(testApp, "v0.224.0")) {
		t.Errorf("<exe> = %q, want the new release", got)
	}
	if got := readFile(t, r.exe+".prev"); got != oldBinary {
		t.Errorf("<exe>.prev = %q, want the previous binary", got)
	}
	if got := r.rst.calls(); len(got) != 0 {
		t.Errorf("Install restarted: %v", got)
	}

	// The same refusals as Apply.
	r2 := applyRig(t, withLegacySelfTestOff(), withMode(ModeOff))
	if err := r2.u.Install(t.Context(), "v0.224.0"); !errors.Is(err, ErrDisabled) {
		t.Errorf("an off updater installed: %v", err)
	}
	r3 := applyRig(t, withLegacySelfTestOff())
	writeIntent(t, restorejournal.PendingPath(r3.state), "reset")
	if err := r3.u.Install(t.Context(), ""); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Errorf("Install under a staged swap = %v, want ErrSwapPending", err)
	}
	r3.assertUntouched()
}
