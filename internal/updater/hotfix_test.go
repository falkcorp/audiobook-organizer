// file: internal/updater/hotfix_test.go
// version: 1.0.1
// guid: 55cb52f7-49f0-4844-881d-0df9ba6269f3
// last-edited: 2026-11-08

package updater

// The R0 updater hotfix (05-updater §5U.12, §5U.11; 09 §9.4 R0): exit 75, the
// kept .prev slot, the semver and local-build rule, refusal of a release that
// is not newer, and check-only unattended runs. Every test runs against an
// httptest release server serving testdata/ and a fake asset, with the
// executable pointed at a temp directory.

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeReleases serves the GitHub release API subset the updater reads.
type fakeReleases struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	latest string          // tag at /releases/latest; "" answers 404
	tags   map[string]bool // tags /releases/tags/<tag> knows

	latestHits atomic.Int32
	assetHits  atomic.Int32
	commitHits atomic.Int32
}

func newFakeReleases(t *testing.T, latest string, tags ...string) *fakeReleases {
	t.Helper()
	f := &fakeReleases{t: t, latest: latest, tags: map[string]bool{}}
	for _, tag := range tags {
		f.tags[tag] = true
	}
	if latest != "" {
		f.tags[latest] = true
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func assetName() string {
	return fmt.Sprintf("audiobook-organizer-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// assetBytes is the fake binary a tag's asset serves.
func assetBytes(tag string) []byte { return []byte("binary for " + tag) }

func (f *fakeReleases) release(tag string) []byte {
	raw, err := os.ReadFile(filepath.Join("testdata", "release.json"))
	if err != nil {
		f.t.Errorf("read release fixture: %v", err)
		return nil
	}
	s := strings.NewReplacer("{{TAG}}", tag, "{{BASE}}", f.srv.URL, "{{ASSET_NAME}}", assetName()).Replace(string(raw))
	return []byte(s)
}

func (f *fakeReleases) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	latest, tags := f.latest, f.tags
	f.mu.Unlock()

	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/releases/latest"):
		f.latestHits.Add(1)
		if latest == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.release(latest))
	case strings.Contains(p, "/releases/tags/"):
		tag := p[strings.LastIndex(p, "/")+1:]
		if !tags[tag] {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.release(tag))
	case strings.HasSuffix(p, "/commits/main"):
		f.commitHits.Add(1)
		raw, err := os.ReadFile(filepath.Join("testdata", "commit_main.json"))
		if err != nil {
			f.t.Errorf("read commit fixture: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	case strings.HasPrefix(p, "/assets/"):
		f.assetHits.Add(1)
		parts := strings.Split(strings.TrimPrefix(p, "/assets/"), "/")
		if len(parts) != 2 || parts[1] != assetName() {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(assetBytes(parts[0]))
	default:
		http.NotFound(w, r)
	}
}

// exitRecorder records every exit code RestartSelf passes.
type exitRecorder struct {
	mu    sync.Mutex
	codes []int
}

func (e *exitRecorder) exit(code int) {
	e.mu.Lock()
	e.codes = append(e.codes, code)
	e.mu.Unlock()
}

func (e *exitRecorder) calls() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.codes...)
}

// hotfixRig is an Updater wired to a fake release server and a temp executable.
type hotfixRig struct {
	u    *Updater
	f    *fakeReleases
	exe  string
	exit *exitRecorder
}

const oldBinary = "the running binary"

func newRig(t *testing.T, current string, f *fakeReleases) *hotfixRig {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "audiobook-organizer")
	if err := os.WriteFile(exe, []byte(oldBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &exitRecorder{}
	u := NewUpdater(current,
		WithAPIBaseForTest(f.srv.URL),
		WithHTTPClientForTest(f.srv.Client()),
		WithExecutableForTest(func() (string, error) { return exe, nil }),
		WithExitForTest(rec.exit),
		WithStateDir(t.TempDir()),
	)
	return &hotfixRig{u: u, f: f, exe: exe, exit: rec}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists (stat err %v), want absent", filepath.Base(path), err)
	}
}

func TestApply_RestartExits75(t *testing.T) {
	rig := newRig(t, "v0.223.0", newFakeReleases(t, "v0.224.0"))

	_ = rig.u.RestartSelf()

	got := rig.exit.calls()
	if len(got) != 1 || got[0] != 75 {
		t.Fatalf("RestartSelf exit codes = %v, want exactly [75]", got)
	}
	if RestartExitCode != 75 {
		t.Fatalf("RestartExitCode = %d, want 75", RestartExitCode)
	}
}

func TestApply_SwapAtomicAndPrevKept(t *testing.T) {
	t.Run("new binary in place, previous kept as .prev, older .prev replaced", func(t *testing.T) {
		rig := newRig(t, "v0.223.0", newFakeReleases(t, "v0.224.0"))
		if err := os.WriteFile(rig.exe+".prev", []byte("an older previous binary"), 0o755); err != nil {
			t.Fatal(err)
		}

		if _, err := rig.u.CheckForUpdate("stable"); err != nil {
			t.Fatalf("check: %v", err)
		}
		target, err := rig.u.PrepareApply("")
		if err != nil {
			t.Fatalf("PrepareApply: %v", err)
		}
		if err := rig.u.DownloadAndReplace(target); err != nil {
			t.Fatalf("DownloadAndReplace: %v", err)
		}

		if got := readFile(t, rig.exe); got != string(assetBytes("v0.224.0")) {
			t.Errorf("<exe> = %q, want the new release's bytes", got)
		}
		if got := readFile(t, rig.exe+".prev"); got != oldBinary {
			t.Errorf("<exe>.prev = %q, want the previously running binary", got)
		}
		assertAbsent(t, rig.exe+".new")
		assertAbsent(t, rig.exe+".old")
		if runtime.GOOS != "windows" {
			st, err := os.Stat(rig.exe)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0o755 {
				t.Errorf("<exe> mode = %v, want 0755", st.Mode().Perm())
			}
		}
		if got := rig.exit.calls(); len(got) != 0 {
			t.Errorf("apply exited with %v; only RestartSelf exits", got)
		}
	})

	t.Run("a failed second rename leaves the old binary at <exe>", func(t *testing.T) {
		rig := newRig(t, "v0.223.0", newFakeReleases(t, "v0.224.0"))
		rig.u.rename = func(oldpath, newpath string) error {
			if oldpath == rig.exe+".new" && newpath == rig.exe {
				return errors.New("injected: second rename failed")
			}
			return os.Rename(oldpath, newpath)
		}

		if _, err := rig.u.CheckForUpdate("stable"); err != nil {
			t.Fatalf("check: %v", err)
		}
		target, err := rig.u.PrepareApply("")
		if err != nil {
			t.Fatalf("PrepareApply: %v", err)
		}
		err = rig.u.DownloadAndReplace(target)
		if err == nil || !strings.Contains(err.Error(), "injected") {
			t.Fatalf("DownloadAndReplace err = %v, want the injected rename failure", err)
		}

		if got := readFile(t, rig.exe); got != oldBinary {
			t.Errorf("<exe> = %q after a failed swap, want the old binary", got)
		}
		assertAbsent(t, rig.exe+".new")
		assertAbsent(t, rig.exe+".old")
	})
}

func TestCheck_SemverNeverDowngrades(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.223.0", "v0.222.9", false},
		{"v0.223.0", "v0.223.0", false},
		{"v0.223.0", "v0.224.0", true},
		{"0.223.0", "v0.222.9", false},
		{"0.223.0", "v0.223.0", false},
		{"0.223.0", "v0.224.0", true},
		{"v0.223.0", "v0.223.10", true}, // numeric, not lexical
		{"v0.222.3-rc.218", "v0.222.3-rc.217", false},
		{"v0.222.3-rc.218", "v0.222.3", true},
		{"dev", "v0.224.0", false},
		{"abc1234", "v0.224.0", false}, // `git describe --always` with no tag
	}
	for _, tc := range cases {
		t.Run(tc.current+"_vs_"+tc.latest, func(t *testing.T) {
			rig := newRig(t, tc.current, newFakeReleases(t, tc.latest))
			info, err := rig.u.CheckForUpdate("stable")
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if info.UpdateAvailable != tc.want {
				t.Errorf("UpdateAvailable = %v, want %v", info.UpdateAvailable, tc.want)
			}
			if info.LocalBuild || info.Note != "" {
				t.Errorf("LocalBuild=%v Note=%q on a non-local version", info.LocalBuild, info.Note)
			}
			if !tc.want {
				if _, err := rig.u.PrepareApply(""); !errors.Is(err, ErrNoUpdate) {
					t.Errorf("unnamed apply err = %v, want ErrNoUpdate (never offered)", err)
				}
			}
		})
	}

	t.Run("apply of a not-newer version is refused and downloads nothing", func(t *testing.T) {
		rig := newRig(t, "v0.223.0", newFakeReleases(t, "v0.224.0", "v0.222.9", "v0.223.0"))
		for _, v := range []string{"v0.222.9", "0.222.9", "v0.223.0", "0.223.0"} {
			if _, err := rig.u.PrepareApply(v); !errors.Is(err, ErrNotNewer) {
				t.Errorf("PrepareApply(%q) err = %v, want ErrNotNewer", v, err)
			}
		}
		// A caller that skips PrepareApply is refused by DownloadAndReplace on
		// the tag it would download.
		err := rig.u.DownloadAndReplace(&UpdateInfo{UpdateAvailable: true, Channel: "stable", LatestVersion: "0.222.9"})
		if !errors.Is(err, ErrNotNewer) {
			t.Errorf("DownloadAndReplace(older) err = %v, want ErrNotNewer", err)
		}
		if n := rig.f.assetHits.Load(); n != 0 {
			t.Errorf("asset downloads = %d after refusals, want 0", n)
		}
		if got := readFile(t, rig.exe); got != oldBinary {
			t.Errorf("<exe> changed by a refused apply: %q", got)
		}
		if _, err := rig.u.PrepareApply("v0.224.0"); err != nil {
			t.Errorf("PrepareApply(newer) err = %v, want allowed", err)
		}
	})

	t.Run("dev refuses a named apply", func(t *testing.T) {
		rig := newRig(t, "dev", newFakeReleases(t, "v0.224.0"))
		if _, err := rig.u.PrepareApply("v0.224.0"); !errors.Is(err, ErrUnversioned) {
			t.Errorf("PrepareApply on dev err = %v, want ErrUnversioned", err)
		}
	})

	t.Run("a malformed version is refused", func(t *testing.T) {
		rig := newRig(t, "v0.223.0", newFakeReleases(t, "v0.224.0"))
		for _, v := range []string{"latest", "../../etc", "v1.2.3/../x", "https://example.invalid/x"} {
			if _, err := rig.u.PrepareApply(v); !errors.Is(err, ErrInvalidVersion) {
				t.Errorf("PrepareApply(%q) err = %v, want ErrInvalidVersion", v, err)
			}
		}
	})
}

func TestCheck_DescribeSuffixedCurrentNotCompared(t *testing.T) {
	currents := []string{"v0.223.0-5-gabc1234", "v0.223.0-dirty", "v0.223.0-5-gabc1234-dirty", "0.223.0-12-g0123abc"}
	latests := []string{"v0.223.0", "v0.222.0", "v0.224.0"}
	for _, cur := range currents {
		for _, latest := range latests {
			t.Run(cur+"_vs_"+latest, func(t *testing.T) {
				rig := newRig(t, cur, newFakeReleases(t, latest, "v0.222.0"))
				info, err := rig.u.CheckForUpdate("stable")
				if err != nil {
					t.Fatalf("check: %v", err)
				}
				if info.UpdateAvailable {
					t.Error("UpdateAvailable on a local build, want nothing offered")
				}
				if !info.LocalBuild {
					t.Error("LocalBuild not set")
				}
				if info.Note != "local build, not compared" {
					t.Errorf("Note = %q, want %q", info.Note, "local build, not compared")
				}
				if _, err := rig.u.PrepareApply(""); !errors.Is(err, ErrLocalBuildNeedsVersion) {
					t.Errorf("unnamed apply err = %v, want ErrLocalBuildNeedsVersion", err)
				}
			})
		}
	}

	t.Run("develop channel never offers an update on a local build", func(t *testing.T) {
		rig := newRig(t, "v0.223.0-5-gabc1234", newFakeReleases(t, "v0.224.0"))
		info, err := rig.u.CheckForUpdate("develop")
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		if info.UpdateAvailable || !info.LocalBuild {
			t.Errorf("develop on a local build: UpdateAvailable=%v LocalBuild=%v", info.UpdateAvailable, info.LocalBuild)
		}
	})

	t.Run("apply of a named version is allowed, even an older one", func(t *testing.T) {
		rig := newRig(t, "v0.223.0-5-gabc1234", newFakeReleases(t, "v0.224.0", "v0.222.0"))
		target, err := rig.u.PrepareApply("0.222.0")
		if err != nil {
			t.Fatalf("PrepareApply(named) err = %v, want allowed", err)
		}
		if err := rig.u.DownloadAndReplace(target); err != nil {
			t.Fatalf("DownloadAndReplace(named): %v", err)
		}
		if got := readFile(t, rig.exe); got != string(assetBytes("v0.222.0")) {
			t.Errorf("<exe> = %q, want the named release's bytes", got)
		}
		if got := readFile(t, rig.exe+".prev"); got != oldBinary {
			t.Errorf("<exe>.prev = %q, want the local build", got)
		}
	})

	t.Run("a named tag the source does not have downloads nothing", func(t *testing.T) {
		rig := newRig(t, "v0.223.0-dirty", newFakeReleases(t, "v0.224.0"))
		target, err := rig.u.PrepareApply("v0.199.0")
		if err != nil {
			t.Fatalf("PrepareApply: %v", err)
		}
		if err := rig.u.DownloadAndReplace(target); !errors.Is(err, ErrReleaseNotFound) {
			t.Errorf("err = %v, want ErrReleaseNotFound", err)
		}
		if n := rig.f.assetHits.Load(); n != 0 {
			t.Errorf("asset downloads = %d, want 0", n)
		}
		if got := readFile(t, rig.exe); got != oldBinary {
			t.Errorf("<exe> changed: %q", got)
		}
	})
}

func TestCheck_ScheduledRunNeverApplies(t *testing.T) {
	rig := newRig(t, "v0.223.0", newFakeReleases(t, "v0.224.0"))
	hour := time.Now().Hour()
	s := NewScheduler(rig.u, func() SchedulerConfig {
		// Inside the window: the hour now is in [hour, hour+1). The window
		// gated HEAD's unattended apply; a check-only tick must not apply
		// inside it either.
		return SchedulerConfig{Enabled: true, Channel: "stable", CheckMins: 60, WindowStart: hour, WindowEnd: (hour + 1) % 24}
	})

	s.tick()

	if n := rig.f.latestHits.Load(); n == 0 {
		t.Fatal("the scheduled run did not check")
	}
	last := rig.u.LastCheck()
	if last == nil {
		t.Fatal("LastCheck is nil after a scheduled run")
	}
	if !last.UpdateAvailable || last.LatestVersion != "0.224.0" {
		t.Errorf("LastCheck = %+v, want the newer release recorded", last)
	}
	if !last.AutoApplyPaused {
		t.Error("status does not report AutoApplyPaused after an unattended run")
	}
	if n := rig.f.assetHits.Load(); n != 0 {
		t.Errorf("asset downloads = %d, want 0 (check only)", n)
	}
	if got := readFile(t, rig.exe); got != oldBinary {
		t.Errorf("<exe> changed by a scheduled run: %q", got)
	}
	assertAbsent(t, rig.exe+".new")
	assertAbsent(t, rig.exe+".prev")
	if got := rig.exit.calls(); len(got) != 0 {
		t.Errorf("exit called with %v by a scheduled run, want never", got)
	}
}
