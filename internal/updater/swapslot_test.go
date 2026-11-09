// file: internal/updater/swapslot_test.go
// version: 1.0.0
// guid: d13289f7-2386-4617-b888-5fb4eb506d55
// last-edited: 2026-11-09

package updater

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// slotRig is an updater with a state directory of its own, so a test can put
// swap intents in it, and a fake release to apply.
func slotRig(t *testing.T, stateDir string) (*Updater, string, *UpdateInfo) {
	t.Helper()
	f := newFakeReleases(t, "v0.224.0")
	exe := filepath.Join(t.TempDir(), "audiobook-organizer")
	if err := os.WriteFile(exe, []byte(oldBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := []Option{
		WithAPIBaseForTest(f.srv.URL),
		WithHTTPClientForTest(f.srv.Client()),
		WithExecutableForTest(func() (string, error) { return exe, nil }),
		WithExitForTest(func(int) {}),
	}
	if stateDir != "" {
		opts = append(opts, WithStateDir(stateDir))
	}
	u := NewUpdater("v0.223.0", opts...)
	if _, err := u.CheckForUpdate("stable"); err != nil {
		t.Fatalf("check: %v", err)
	}
	target, err := u.PrepareApply("")
	if err != nil {
		t.Fatalf("PrepareApply: %v", err)
	}
	return u, exe, target
}

func writeIntent(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"20261108T000000Z-0a0b0c0d","kind":"reset"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRefusesWhileSwapPendingOrInflight(t *testing.T) {
	t.Run("an empty slot proceeds past the check", func(t *testing.T) {
		u, exe, target := slotRig(t, t.TempDir())
		if err := u.DownloadAndReplace(target); err != nil {
			t.Fatalf("DownloadAndReplace with an empty slot: %v", err)
		}
		if got := readFile(t, exe); got != string(assetBytes("v0.224.0")) {
			t.Errorf("<exe> = %q, want the new release's bytes", got)
		}
	})

	t.Run("a staged swap refuses with restore_pending and touches nothing", func(t *testing.T) {
		state := t.TempDir()
		writeIntent(t, restorejournal.PendingPath(state))
		u, exe, target := slotRig(t, state)

		err := u.DownloadAndReplace(target)
		if err == nil || !strings.Contains(err.Error(), restorejournal.TokenRestorePending) {
			t.Fatalf("err = %v, want one carrying %s", err, restorejournal.TokenRestorePending)
		}
		if !errors.Is(err, ErrSwapBusy) {
			t.Errorf("err = %v, want it to match ErrSwapBusy", err)
		}
		assertBinaryUntouched(t, exe)
	})

	t.Run("an in-flight swap refuses with restore_inflight and touches nothing", func(t *testing.T) {
		state := t.TempDir()
		writeIntent(t, filepath.Join(restorejournal.JournalDir(state, "20261108T000000Z-0a0b0c0d"), "inflight.json"))
		u, exe, target := slotRig(t, state)

		err := u.DownloadAndReplace(target)
		if err == nil || !strings.Contains(err.Error(), restorejournal.TokenRestoreInflight) {
			t.Fatalf("err = %v, want one carrying %s", err, restorejournal.TokenRestoreInflight)
		}
		if !errors.Is(err, ErrSwapBusy) {
			t.Errorf("err = %v, want it to match ErrSwapBusy", err)
		}
		assertBinaryUntouched(t, exe)
	})

	t.Run("a swap staged during the download refuses before the rename", func(t *testing.T) {
		state := t.TempDir()
		u, exe, target := slotRig(t, state)
		// The asset download stages a reset in the slot as it starts, as an
		// operator's storage reset racing the update would.
		staging := &stageOnDownload{next: u.httpClient.Transport, pending: restorejournal.PendingPath(state), t: t}
		u.httpClient = &http.Client{Transport: staging}

		err := u.DownloadAndReplace(target)
		if !staging.done {
			t.Fatal("the release was never downloaded")
		}
		if err == nil || !strings.Contains(err.Error(), restorejournal.TokenRestorePending) {
			t.Fatalf("err = %v, want the re-check before the rename to refuse with %s", err, restorejournal.TokenRestorePending)
		}
		assertBinaryUntouched(t, exe)
	})

	t.Run("a slot that cannot be read refuses and touches nothing", func(t *testing.T) {
		// pending.json as a directory: it opens, and reading it fails.
		state := t.TempDir()
		if err := os.MkdirAll(restorejournal.PendingPath(state), 0o700); err != nil {
			t.Fatal(err)
		}
		u, exe, target := slotRig(t, state)

		err := u.DownloadAndReplace(target)
		if err == nil || !strings.Contains(err.Error(), "cannot read the swap slot") {
			t.Fatalf("err = %v, want the unreadable-slot refusal", err)
		}
		assertBinaryUntouched(t, exe)
	})
}

// An updater built without a state directory cannot read the slot, so it
// refuses instead of skipping the check.
func TestApplyRefusesWithoutStateDir(t *testing.T) {
	u, exe, target := slotRig(t, "")
	if err := u.DownloadAndReplace(target); !errors.Is(err, ErrNoStateDir) {
		t.Fatalf("err = %v, want ErrNoStateDir", err)
	}
	assertBinaryUntouched(t, exe)
}

// stageOnDownload writes an intent into the slot when the release asset's
// download starts, then serves it.
type stageOnDownload struct {
	next    http.RoundTripper
	pending string
	t       *testing.T
	done    bool
}

func (s *stageOnDownload) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Accept") == "application/octet-stream" && !s.done {
		s.done = true
		writeIntent(s.t, s.pending)
	}
	return s.next.RoundTrip(r)
}

func assertBinaryUntouched(t *testing.T, exe string) {
	t.Helper()
	if got := readFile(t, exe); got != oldBinary {
		t.Errorf("<exe> = %q, want the running binary untouched", got)
	}
	assertAbsent(t, exe+".new")
	assertAbsent(t, exe+".prev")
}
