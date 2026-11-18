// file: core/updater/check_test.go
// version: 1.0.0
// guid: 2c7e90b4-6a15-4d38-8e01-f5b3a94d7c62
// last-edited: 2026-11-18

//go:build unix

package updater

// 05-updater 5U.11's check rows: the semver rule, a dev build, a local
// (describe-suffixed) build, and the check-only scheduled run.

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/falkcorp/mediacore/health"
)

func TestCheck_SemverNeverDowngrades(t *testing.T) {
	cases := []struct {
		name, current, latest string
		available             bool
	}{
		{"newer is offered", "v0.223.0", "v0.224.0", true},
		{"the same version is not an update", "v0.224.0", "v0.224.0", false},
		{"an older release is not an update (string inequality was HEAD's defect)", "v0.224.0", "v0.223.0", false},
		{"semver, not text, orders 0.9 below 0.10", "v0.9.0", "v0.10.0", true},
		{"a bare current version compares as a tag", "0.223.0", "v0.224.0", true},
		{"a release candidate of the next version is newer than the last stable", "v0.223.0", "v0.224.0-rc.1", true},
		{"an unparsable latest is never newer", "v0.223.0", "nightly", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := newFakeSource()
			src.rels[tc.latest] = Release{Version: tc.latest, Assets: nil}
			src.latest[ChannelStable] = tc.latest
			r := newRig(t, tc.current, src)

			st, err := r.u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if st.Available != tc.available {
				t.Errorf("Available = %v, want %v (current %s, latest %s)", st.Available, tc.available, tc.current, tc.latest)
			}
			if st.Latest != tc.latest {
				t.Errorf("Latest = %q, want %q", st.Latest, tc.latest)
			}
			if r.u.Status().Available != st.Available {
				t.Error("Status() does not hold what Check returned")
			}
		})
	}
}

func TestCheck_DevBuildNeverAvailable(t *testing.T) {
	for _, current := range []string{"dev", "", "not a version"} {
		t.Run("current="+current, func(t *testing.T) {
			src := newFakeSource()
			src.add("v9.9.9", fakeBinary(testApp, "v9.9.9"), true)
			r := newRig(t, "dev", src)
			r.u.version = current // New refuses an empty version; the field is what Check reads

			st, err := r.u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if st.Available {
				t.Error("a build with no release version was offered an update")
			}
			if st.DisabledReason != "dev_build" {
				t.Errorf("DisabledReason = %q, want dev_build", st.DisabledReason)
			}
			if _, err := r.u.Prepare(""); !errors.Is(err, ErrNoUpdate) {
				t.Errorf("Prepare(\"\") = %v, want ErrNoUpdate", err)
			}
			if _, err := r.u.Prepare("v9.9.9"); !errors.Is(err, ErrUnversioned) {
				t.Errorf("Prepare(v9.9.9) = %v, want ErrUnversioned", err)
			}
		})
	}
}

func TestCheck_DescribeSuffixedCurrentNotCompared(t *testing.T) {
	for _, current := range []string{"v0.223.0-5-gabc1234", "v0.223.0-5-gabc1234-dirty", "v0.223.0-dirty"} {
		for _, latest := range []string{"v0.223.0", "v0.222.0", "v0.224.0"} {
			t.Run(current+" vs "+latest, func(t *testing.T) {
				src := newFakeSource()
				src.add(latest, fakeBinary(testApp, latest), true)
				r := newRig(t, current, src)

				st, err := r.u.Check(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if st.Available {
					t.Errorf("a local build %s was offered %s", current, latest)
				}
				if !st.LocalBuild {
					t.Error("LocalBuild is not set")
				}
				if got := st.View(); got.LocalBuildNote != LocalBuildNote || !got.LocalBuild {
					t.Errorf("view = %+v, want the %q note", got, LocalBuildNote)
				}
				if _, err := r.u.Prepare(""); !errors.Is(err, ErrLocalBuildNeedsVersion) {
					t.Errorf("an unnamed apply = %v, want ErrLocalBuildNeedsVersion", err)
				}
				tgt, err := r.u.Prepare(latest)
				if err != nil {
					t.Fatalf("a named apply of %s on a local build: %v", latest, err)
				}
				if !tgt.Named || tgt.Version != latest {
					t.Errorf("target = %+v, want the named %s", tgt, latest)
				}
			})
		}
	}
}

func TestCheck_ScheduledRunNeverApplies(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	set := DefaultSettings(ModeCheck)
	set.AutoApplyPaused = true
	r := newRig(t, "v0.223.0", src, withSettings(func() Settings { return set }))

	st, err := r.u.runCheck(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available || st.Latest != "0.224.0" {
		t.Fatalf("view = %+v, want the newer release recorded", st)
	}
	if !st.AutoApplyPaused {
		t.Error("an operator who asked for automatic apply did not get AutoApplyPaused")
	}
	if got := src.openedNames(); len(got) != 0 {
		t.Errorf("a scheduled check opened assets %v: it downloaded", got)
	}
	r.assertUntouched()

	res := r.u.Probe(context.Background())
	if res.Status != health.StatusDegraded || res.Reason != "auto_update_paused" {
		t.Errorf("probe = %v/%q, want Degraded/auto_update_paused", res.Status, res.Reason)
	}
}

func TestCheck_OffMakesNoNetworkCall(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	r := newRig(t, "v0.223.0", src, withMode(ModeOff))

	st, err := r.u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := src.latestCalls(); n != 0 {
		t.Errorf("mode off asked the source %d times", n)
	}
	if st.Available || st.DisabledReason != "off" {
		t.Errorf("status = %+v, want off and nothing offered", st)
	}
	if res := r.u.Probe(context.Background()); res.Status != health.StatusUp || res.Reason != "off" {
		t.Errorf("probe = %v/%q, want Up/off", res.Status, res.Reason)
	}
}

func TestCheck_SourceFailureIsOpen(t *testing.T) {
	src := newFakeSource() // Latest answers ErrReleaseNotFound: nothing published yet
	r := newRig(t, "v0.223.0", src)

	st, err := r.u.Check(context.Background())
	if err != nil || st.Available || st.LastError != "" {
		t.Fatalf("an empty source: status %+v, err %v; want a quiet no-update", st, err)
	}
	if res := r.u.Probe(context.Background()); res.Status != health.StatusUp {
		t.Errorf("probe after an empty source = %v, want Up", res.Status)
	}

	r2 := newRig(t, "v0.223.0", newFakeSource(), withSource(failingSource{}))
	st, err = r2.u.Check(context.Background())
	if err == nil {
		t.Fatal("a failing source returned no error")
	}
	if st.LastError != "check_failed" {
		t.Errorf("LastError = %q, want the fixed token check_failed", st.LastError)
	}
	if res := r2.u.Probe(context.Background()); res.Status != health.StatusDegraded || res.Reason != "check_failed" {
		t.Errorf("probe = %v/%q, want Degraded/check_failed", res.Status, res.Reason)
	}
}

// failingSource fails every lookup.
type failingSource struct{}

func (failingSource) Latest(context.Context, Channel) (Release, error) {
	return Release{}, errors.New("the source is down")
}

func (failingSource) Open(context.Context, Asset) (io.ReadCloser, int64, error) {
	return nil, -1, errors.New("the source is down")
}

func TestCheck_NoAssetIsReportedNotFatal(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	rel := src.rels["v0.224.0"]
	rel.Assets = []Asset{{Name: "gotham-hq-plan9-mips", URL: "mem://x"}}
	src.rels["v0.224.0"] = rel
	r := newRig(t, "v0.223.0", src)

	st, err := r.u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available || st.LastError != "no_asset" {
		t.Errorf("status = %+v, want available with LastError no_asset", st)
	}
}
