// file: core/updater/rollback_test.go
// version: 1.0.0
// guid: b94c7e12-5a30-4f8d-8c6b-2d1e07a93f45
// last-edited: 2026-11-18

//go:build unix

package updater

// 05-updater 5U.6 and 5U.7: rollback is the in-process port of `make
// rollback`; fetch-release is the one thing `make rollback-restore` needs
// from the updater.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rollbackRig has a kept previous binary beside the running one.
func rollbackRig(t *testing.T, reader ReaderCheck) *rig {
	t.Helper()
	r := newRig(t, "v0.224.0", newFakeSource(), withReader(reader))
	if err := os.WriteFile(r.exe+".prev", []byte("the previous binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRollback_ReaderRefusesPrev(t *testing.T) {
	for _, reason := range []string{ReasonLevelNewer, ReasonFormatNewer, ReasonRestorePending, ReasonRestoreInflight, ReasonSidecarUnreadable, ReasonCannotDetermine} {
		t.Run(reason, func(t *testing.T) {
			var gotDB, gotExe string
			r := rollbackRig(t, func(_ context.Context, db, exe string) error {
				gotDB, gotExe = db, exe
				return &Refusal{Reason: reason, Detail: "no"}
			})
			r.u.dbPath = "/batcave/main.pebble"

			err := r.u.Rollback(r.rc(KindRollback))
			ref, ok := IsRefusal(err)
			if !ok || ref.Reason != reason {
				t.Fatalf("err = %v, want a refusal with reason %s", err, reason)
			}
			if gotDB != "/batcave/main.pebble" || gotExe != r.exe+".prev" {
				t.Errorf("the check ran on (%q, %q), want the store and <exe>.prev", gotDB, gotExe)
			}
			if got := readFile(t, r.exe); got != oldBinary {
				t.Errorf("<exe> = %q, want the running binary untouched", got)
			}
			if got := readFile(t, r.exe+".prev"); got != "the previous binary" {
				t.Errorf("<exe>.prev = %q, want it kept", got)
			}
			assertAbsent(t, r.exe+".rolled-back")
			if got := r.rst.calls(); len(got) != 0 {
				t.Errorf("restarts = %v, want none", got)
			}
			if reason == ReasonLevelNewer || reason == ReasonFormatNewer {
				if !strings.Contains(err.Error(), "make rollback-restore") || !strings.Contains(err.Error(), "RELEASE=") {
					t.Errorf("err = %v, want it to point at `make rollback-restore RELEASE=<tag>`", err)
				}
			}
		})
	}
}

func TestRollback_SwapsPrevBack(t *testing.T) {
	r := rollbackRig(t, func(context.Context, string, string) error { return nil })

	if err := r.u.Rollback(r.rc(KindRollback)); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, r.exe); got != "the previous binary" {
		t.Errorf("<exe> = %q, want the previous binary", got)
	}
	if got := readFile(t, r.exe+".rolled-back"); got != oldBinary {
		t.Errorf("<exe>.rolled-back = %q, want the binary that was running (make rollback's name for it)", got)
	}
	assertAbsent(t, r.exe+".prev")
	if got := r.rst.calls(); len(got) != 1 || got[0] != restartReasonRollback {
		t.Errorf("restarts = %v, want one with reason %q", got, restartReasonRollback)
	}
	if st := r.u.Status(); st.PreviousKept != "" {
		t.Errorf("PreviousKept = %q after the previous binary was consumed", st.PreviousKept)
	}
}

func TestRollback_Refusals(t *testing.T) {
	t.Run("no previous binary", func(t *testing.T) {
		r := newRig(t, "v0.224.0", newFakeSource(), withReader(func(context.Context, string, string) error { return nil }))
		if err := r.u.Rollback(r.rc(KindRollback)); !errors.Is(err, ErrNoPrevious) {
			t.Fatalf("err = %v, want ErrNoPrevious", err)
		}
	})
	t.Run("no reader check cannot determine, never passes", func(t *testing.T) {
		r := rollbackRig(t, nil)
		err := r.u.Rollback(r.rc(KindRollback))
		if ref, ok := IsRefusal(err); !ok || ref.Reason != ReasonCannotDetermine {
			t.Fatalf("err = %v, want a cannot_determine refusal", err)
		}
		if got := readFile(t, r.exe); got != oldBinary {
			t.Errorf("<exe> = %q: a rollback with no check swapped anyway", got)
		}
	})
	t.Run("a failed second rename puts the running binary back", func(t *testing.T) {
		r := rollbackRig(t, func(context.Context, string, string) error { return nil })
		calls := 0
		r.u.rename = func(a, b string) error {
			calls++
			if calls == 2 {
				return errors.New("disk on fire")
			}
			return os.Rename(a, b)
		}
		if err := r.u.Rollback(r.rc(KindRollback)); err == nil {
			t.Fatal("a failed swap was reported as success")
		}
		if got := readFile(t, r.exe); got != oldBinary {
			t.Errorf("<exe> = %q, want the running binary restored", got)
		}
		if got := readFile(t, r.exe+".prev"); got != "the previous binary" {
			t.Errorf("<exe>.prev = %q, want it kept", got)
		}
	})
}

// TestFetchRelease_NamedReleaseOnly: "latest", a URL and a path are not
// release names; a name the source does not have fetches nothing.
func TestFetchRelease_NamedReleaseOnly(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	r := newRig(t, "v0.223.0", src)
	dest := filepath.Join(t.TempDir(), "fetched")

	for _, v := range []string{"", "latest", "LATEST", "../v0.224.0", "https://example.invalid/x", "v0.224", "0.224.0/../x"} {
		if err := r.u.FetchRelease(t.Context(), v, dest); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("FetchRelease(%q) = %v, want ErrInvalidVersion", v, err)
		}
	}
	if err := r.u.FetchRelease(t.Context(), "v0.300.0", dest); !errors.Is(err, ErrReleaseNotFound) {
		t.Errorf("an unknown tag: %v, want ErrReleaseNotFound", err)
	}
	if got := src.openedNames(); len(got) != 0 {
		t.Errorf("a refused name still downloaded %v", got)
	}
	assertAbsent(t, dest)

	// The named release, with and without the leading v.
	for _, v := range []string{"v0.224.0", "0.224.0"} {
		d := filepath.Join(t.TempDir(), "fetched")
		if err := r.u.FetchRelease(t.Context(), v, d); err != nil {
			t.Fatalf("FetchRelease(%q): %v", v, err)
		}
		if got := readFile(t, d); got != string(fakeBinary(testApp, "v0.224.0")) {
			t.Errorf("fetched body = %q", got)
		}
		fi, _ := os.Stat(d)
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("fetched mode = %v, want 0755", fi.Mode().Perm())
		}
		assertAbsent(t, d+".part")
	}
	// It installed nothing.
	r.assertUntouched()
	if st := r.u.Status(); st.PreviousKept != "" {
		t.Errorf("a fetch changed the status: %+v", st)
	}
}

func TestFetchRelease_FailureLeavesNothing(t *testing.T) {
	cases := map[string]func(*fakeSource, string){
		"download fails half way": func(s *fakeSource, url string) { s.midErr[url] = errors.New("reset") },
		"body is the wrong app":   func(s *fakeSource, url string) { s.bodies[url] = fakeBinary("metropolis", "v0.224.0") },
		"body is the wrong tag":   func(s *fakeSource, url string) { s.bodies[url] = fakeBinary(testApp, "v0.100.0") },
		"body is not a program":   func(s *fakeSource, url string) { s.bodies[url] = []byte("not a program") },
		"body is over the cap": func(s *fakeSource, url string) {
			s.sizes[url] = DefaultMaxDownload + 1
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			src := newFakeSource()
			src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
			mutate(src, src.rels["v0.224.0"].Assets[0].URL)
			r := newRig(t, "v0.223.0", src)
			dest := filepath.Join(t.TempDir(), "fetched")

			if err := r.u.FetchRelease(t.Context(), "v0.224.0", dest); err == nil {
				t.Fatal("a failed fetch returned nil")
			}
			assertAbsent(t, dest)
			assertAbsent(t, dest+".part")
			r.assertUntouched()
		})
	}
}

func TestFetchRelease_RefusesTheRunningBinarySlots(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	r := newRig(t, "v0.223.0", src)
	for _, dest := range []string{r.exe, r.exe + ".prev"} {
		if err := r.u.FetchRelease(t.Context(), "v0.224.0", dest); err == nil {
			t.Errorf("a fetch into %s was allowed", filepath.Base(dest))
		}
	}
	r.assertUntouched()
	if got := src.openedNames(); len(got) != 0 {
		t.Errorf("a refused destination still downloaded %v", got)
	}
}
