// file: core/updater/readercheck_test.go
// version: 1.0.0
// guid: f5a3c807-2e19-4d64-b0a8-91c6e3d52b70
// last-edited: 2026-11-18

//go:build unix

package updater

// NewReaderCheck is the in-process port of `make rollback`'s guard decision
// (scripts/storage_format_guard.py decide_rollback). These cases are that
// script's decision table, run against an in-memory filesystem and a fake
// binary runner.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

const (
	rcDB    = "/batcave/main.pebble"
	rcState = "/batcave/state"
	rcExe   = "/opt/gotham/gotham-hq.prev"
)

// runResult is one canned answer of the fake runner.
type runResult struct {
	out, errOut string
	code        int
	err         error
}

// fakeRun answers by the flag it is given and records the calls.
func fakeRun(answers map[string]runResult, calls *[]string) func(context.Context, string, ...string) (string, string, int, error) {
	return func(_ context.Context, exe string, args ...string) (string, string, int, error) {
		*calls = append(*calls, exe+" "+strings.Join(args, " "))
		a, ok := answers[strings.Join(args, " ")]
		if !ok {
			return "", "", 1, nil
		}
		return a.out, a.errOut, a.code, a.err
	}
}

type rcRig struct {
	fs    vfs.FS
	calls []string
}

func newRCRig(t *testing.T) *rcRig {
	t.Helper()
	fs := vfs.NewMem()
	for _, d := range []string{"/batcave", rcState} {
		if err := fs.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &rcRig{fs: fs}
}

func (r *rcRig) check(t *testing.T, stateDir string, answers map[string]runResult) error {
	t.Helper()
	c := NewReaderCheck(ReaderCheckOptions{StateDir: stateDir, FS: r.fs, Run: fakeRun(answers, &r.calls)})
	return c(t.Context(), rcDB, rcExe)
}

func levels(t *testing.T, r *rcRig, l map[string]int) {
	t.Helper()
	if err := pebble.WriteLevels(r.fs, rcDB, l); err != nil {
		t.Fatal(err)
	}
}

func wantReason(t *testing.T, err error, reason string) *Refusal {
	t.Helper()
	ref, ok := IsRefusal(err)
	if !ok {
		t.Fatalf("err = %v, want a *Refusal with reason %s", err, reason)
	}
	if ref.Reason != reason {
		t.Fatalf("reason = %s (%s), want %s", ref.Reason, ref.Detail, reason)
	}
	return ref
}

func servesLevels(json string) map[string]runResult {
	return map[string]runResult{"--print-storage-levels": {out: json + "\n"}}
}

func TestNewReaderCheck_LevelRule(t *testing.T) {
	t.Run("the binary serves what the store holds", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"books": 2, "tasks": 1})
		if err := r.check(t, rcState, servesLevels(`{"books":2,"tasks":1,"auth":3}`)); err != nil {
			t.Fatalf("a binary at or above every store level was refused: %v", err)
		}
	})
	t.Run("a level above what the binary serves refuses level_newer, naming the owners", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"books": 3, "tasks": 1, "auth": 2})
		ref := wantReason(t, r.check(t, rcState, servesLevels(`{"books":2,"tasks":1}`)), ReasonLevelNewer)
		for _, want := range []string{"auth", "books"} {
			if !strings.Contains(ref.Detail, want) {
				t.Errorf("detail %q does not name %s", ref.Detail, want)
			}
		}
		if strings.Contains(ref.Detail, "tasks,") {
			t.Errorf("detail %q names an owner that is not above", ref.Detail)
		}
	})
	t.Run("an owner the binary does not know is at level 0", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"oracle": 1})
		wantReason(t, r.check(t, rcState, servesLevels(`{"books":2}`)), ReasonLevelNewer)
	})
	t.Run("a store owner at 0 passes a binary that knows nothing of it", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"oracle": 0})
		if err := r.check(t, rcState, servesLevels(`{}`)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNewReaderCheck_BinaryThatPredatesLevels(t *testing.T) {
	oldLevels := runResult{errOut: "Error: unknown flag: --print-storage-levels\n", code: 1}
	t.Run("format 1 serves every owner at 0", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"books": 0})
		if err := r.check(t, rcState, map[string]runResult{"--print-storage-levels": oldLevels, "--print-storage-format": {out: "1\n"}}); err != nil {
			t.Fatalf("store at level 0 refused by a format-1 binary: %v", err)
		}
		levels(t, r, map[string]int{"books": 1})
		wantReason(t, r.check(t, rcState, map[string]runResult{"--print-storage-levels": oldLevels, "--print-storage-format": {out: "1\n"}}), ReasonLevelNewer)
	})
	t.Run("a binary older than both flags is format 1", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"books": 0})
		both := map[string]runResult{"--print-storage-levels": oldLevels, "--print-storage-format": {errOut: "unknown flag: --print-storage-format", code: 1}}
		if err := r.check(t, rcState, both); err != nil {
			t.Fatalf("refused: %v", err)
		}
	})
	t.Run("any other format answer is not guessed", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"books": 0})
		wantReason(t, r.check(t, rcState, map[string]runResult{"--print-storage-levels": oldLevels, "--print-storage-format": {out: "2\n"}}), ReasonCannotDetermine)
		wantReason(t, r.check(t, rcState, map[string]runResult{"--print-storage-levels": oldLevels, "--print-storage-format": {out: "banana"}}), ReasonCannotDetermine)
	})
	t.Run("a levels failure that is not the unknown flag is not read as old", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"books": 0})
		wantReason(t, r.check(t, rcState, map[string]runResult{"--print-storage-levels": {errOut: "panic: boom", code: 2}}), ReasonCannotDetermine)
	})
}

func TestNewReaderCheck_CannotDetermine(t *testing.T) {
	r := newRCRig(t)
	levels(t, r, map[string]int{"books": 1})
	for name, ans := range map[string]map[string]runResult{
		"the binary cannot run":        {"--print-storage-levels": {err: errors.New("exec format error")}},
		"prints no JSON":               servesLevels("hello"),
		"prints a non-object":          servesLevels("[1,2]"),
		"a level that is a string":     servesLevels(`{"books":"2"}`),
		"a negative level":             servesLevels(`{"books":-1}`),
		"a fractional level":           servesLevels(`{"books":1.5}`),
		"an owner name out of grammar": servesLevels(`{"Books/../x":9}`),
	} {
		t.Run(name, func(t *testing.T) { wantReason(t, r.check(t, rcState, ans), ReasonCannotDetermine) })
	}
	t.Run("no state directory", func(t *testing.T) {
		ref := wantReason(t, r.check(t, "", servesLevels(`{"books":9}`)), ReasonCannotDetermine)
		if !errors.Is(ref, ErrNoStateDir) {
			t.Errorf("refusal does not unwrap to ErrNoStateDir: %v", ref)
		}
	})
}

func TestNewReaderCheck_LevelsFileRules(t *testing.T) {
	t.Run("an unreadable levels file is sidecar_unreadable", func(t *testing.T) {
		r := newRCRig(t)
		f, _ := r.fs.Create(rcDB+pebble.LevelsSuffix, vfs.WriteCategoryUnspecified)
		_, _ = f.Write([]byte("books banana\n"))
		_ = f.Close()
		wantReason(t, r.check(t, rcState, servesLevels(`{"books":9}`)), ReasonSidecarUnreadable)
	})
	t.Run("a missing levels file beside a checkpoint pointer is sidecar_unreadable, not an old store", func(t *testing.T) {
		r := newRCRig(t)
		if err := pebble.WriteFileAtomic(r.fs, rcDB+pebble.CheckpointSuffix, []byte("ptr")); err != nil {
			t.Fatal(err)
		}
		wantReason(t, r.check(t, rcState, servesLevels(`{}`)), ReasonSidecarUnreadable)
	})
	t.Run("a store from before core: HEAD's projection sidecar against the binary's format", func(t *testing.T) {
		r := newRCRig(t)
		if err := pebble.WriteInt(r.fs, rcDB+".storage-format", 1); err != nil {
			t.Fatal(err)
		}
		if err := r.check(t, rcState, servesLevels(`{}`)); err != nil {
			t.Fatalf("format 1 store, format 1 binary: %v", err)
		}
		if err := pebble.WriteInt(r.fs, rcDB+".storage-format", 2); err != nil {
			t.Fatal(err)
		}
		wantReason(t, r.check(t, rcState, servesLevels(`{}`)), ReasonFormatNewer)
		if err := r.check(t, rcState, servesLevels(`{"books":1}`)); err != nil {
			t.Fatalf("format 2 store, a binary serving a level: %v", err)
		}
	})
	t.Run("neither file: sidecar_unreadable", func(t *testing.T) {
		r := newRCRig(t)
		wantReason(t, r.check(t, rcState, servesLevels(`{}`)), ReasonSidecarUnreadable)
	})
}

func TestNewReaderCheck_SwapSlotRefusesFirstAndWithoutRunningAnything(t *testing.T) {
	t.Run("a staged swap", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"books": 1})
		put(t, r.fs, restorejournal.PendingPath(rcState), "reset")
		ref := wantReason(t, r.check(t, rcState, servesLevels(`{"books":1}`)), ReasonRestorePending)
		if !errors.Is(ref, restorejournal.ErrSwapPending) {
			t.Errorf("refusal does not unwrap to ErrSwapPending: %v", ref)
		}
		if len(r.calls) != 0 {
			t.Errorf("the slot refusal still ran the binary: %v", r.calls)
		}
	})
	t.Run("an in-flight swap", func(t *testing.T) {
		r := newRCRig(t)
		levels(t, r, map[string]int{"books": 1})
		put(t, r.fs, filepath.Join(restorejournal.JournalDir(rcState, "20261108T000000Z-0a0b0c0d"), "inflight.json"), "checkpoint")
		wantReason(t, r.check(t, rcState, servesLevels(`{"books":1}`)), ReasonRestoreInflight)
	})
}

func put(t *testing.T, fs vfs.FS, path, kind string) {
	t.Helper()
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := fs.Create(path, vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte(`{"id":"20261108T000000Z-0a0b0c0d","kind":"` + kind + `"}`))
	_ = f.Close()
}

// TestNewReaderCheck_RunsARealBinary: with no Run substituted, the check runs
// the binary for real, with a scrubbed environment.
func TestNewReaderCheck_RunsARealBinary(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "main.pebble")
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pebble.WriteLevels(vfs.Default, db, map[string]int{"books": 2}); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "gotham-hq.prev")
	// It prints a vector only when a secret in its environment is absent.
	script := scriptBinary(`if [ -n "$BATCAVE_SECRET" ]; then echo '{"books":0}'; else echo '{"books":2}'; fi`)
	if err := os.WriteFile(exe, script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATCAVE_SECRET", "batarang")

	check := NewReaderCheck(ReaderCheckOptions{StateDir: state})
	if err := check(t.Context(), db, exe); err != nil {
		t.Fatalf("a binary serving level 2 for a store at 2: %v (the environment leaked into the probe?)", err)
	}
	if err := os.WriteFile(exe, scriptBinary(`echo '{"books":1}'`), 0o755); err != nil {
		t.Fatal(err)
	}
	wantReason(t, check(t.Context(), db, exe), ReasonLevelNewer)
}
