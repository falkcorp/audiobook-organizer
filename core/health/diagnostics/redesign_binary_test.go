// file: core/health/diagnostics/redesign_binary_test.go
// version: 1.1.0
// guid: 8d3f5b72-6a14-4c90-b7e2-1f9a4d6c0e53
// last-edited: 2026-11-15

package diagnostics

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/archtest"
)

// goTool is the go command that built this test binary's toolchain: the one on
// PATH, else GOROOT's.
func goTool(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("no go command on PATH or in GOROOT: %v", err)
	}
	return p
}

// moduleRoot is the directory with go.mod above the test's working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// TestRedesignRefusal_RealBinary (round 10, N-3; round 11, O-1) builds a small
// main that imports this package and runs it as a production binary
// (testing.Testing() is false in it), then checks that diagnostics.New and the
// zero Exporter's Build both return ErrRedesignPendingF8_03b. Every other test of
// the refusal passes the probe in, so a probe that always said "test binary" (New
// passing func() bool { return true }, or testBinary's fallback returning true)
// survived them all; both die here, because the binary reaches the probe the way
// a production caller does.
//
// It does this once per tag set the repo ships with (archtest.F803bShippedTagSets:
// the one TagSets() list plus every -tags list the Makefiles, Dockerfiles and
// workflows name), so a build-tagged copy of the marker, New or Build that the
// default build never sees is run too: the production image builds with
// embed_frontend. The sets are the ones this host can run (a GOOS other than the
// host's is built by CI for another machine; its files are read by the gate's
// constraint rule instead). Not skipped under -short: this is the one test that
// proves the refusal works in the process it exists for. It costs one cached
// go build and link per set.
func TestRedesignRefusal_RealBinary(t *testing.T) {
	if !RedesignPendingF8_03b {
		t.Fatal("the marker is false; F8-03b deletes this test with the marker")
	}
	goBin, root := goTool(t), moduleRoot(t)
	sets, named, err := archtest.F803bShippedTagSets(root)
	if err != nil {
		t.Fatal(err)
	}
	// The reading of the build files must have found the tag sets it is for, or
	// the test would run the default build alone and pass.
	if named == 0 {
		t.Fatal("no -tags list was read from the repo's build files; the real-binary test would build only the default set")
	}
	var ran []string
	for _, ts := range sets {
		if ts.GOOS != "" && ts.GOOS != runtime.GOOS {
			continue
		}
		ran = append(ran, ts.Name)
		t.Run(ts.Name, func(t *testing.T) { realBinaryRefuses(t, goBin, root, ts) })
	}
	for _, want := range []string{"default", "image"} {
		if !slices.Contains(ran, want) {
			t.Errorf("tag set %q was not built; built %v", want, ran)
		}
	}
	if !slices.ContainsFunc(sets, func(ts archtest.TagSet) bool { return slices.Contains(ts.Tags, "embed_frontend") && ts.GOOS == "" }) {
		t.Errorf("no host tag set with embed_frontend, the production image's tag: %v", ran)
	}
}

// realBinaryRefuses builds testdata/probemain under ts and runs it.
func realBinaryRefuses(t *testing.T, goBin, root string, ts archtest.TagSet) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "probemain")
	// -buildvcs=false: the child build must not stamp VCS, which fails in a
	// container whose checkout git calls dubious.
	args := []string{"build", "-buildvcs=false", "-o", bin}
	// "race" is not a tag a build may name: -tags race selects the race
	// runtime's files without the runtime and fails to link. The go command defines
	// the tag itself under -race, so the set is built that way.
	tags := slices.DeleteFunc(slices.Clone(ts.Tags), func(tag string) bool { return tag == "race" })
	if len(tags) != len(ts.Tags) {
		args = append(args, "-race")
	}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./health/diagnostics/testdata/probemain")
	build := exec.CommandContext(ctx, goBin, args...)
	build.Dir = root
	build.Env = os.Environ()
	if ts.GOOS != "" {
		build.Env = append(build.Env, "GOOS="+ts.GOOS)
	}
	if ts.CGOEnabled != "" {
		build.Env = append(build.Env, "CGO_ENABLED="+ts.CGOEnabled)
	}
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build of the probe binary under tag set %q (tags %v): %v\n%s", ts.Name, ts.Tags, err, out)
	}
	// The binary must carry the build settings the set names, or the run below
	// proves the default build and nothing else.
	info, err := exec.CommandContext(ctx, goBin, "version", "-m", bin).CombinedOutput()
	if err != nil {
		t.Fatalf("go version -m %s: %v\n%s", bin, err, info)
	}
	if len(tags) > 0 && !strings.Contains(string(info), "-tags="+strings.Join(tags, ",")) {
		t.Fatalf("the binary for tag set %q was not built with -tags=%s:\n%s", ts.Name, strings.Join(tags, ","), info)
	}
	if len(tags) != len(ts.Tags) && !strings.Contains(string(info), "-race=true") {
		t.Fatalf("the binary for tag set %q was not built with -race:\n%s", ts.Name, info)
	}
	out, err := exec.CommandContext(ctx, bin).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "refused" {
		t.Fatalf("the production binary built under tag set %q (tags %v) did not refuse: err = %v\n%s", ts.Name, ts.Tags, err, out)
	}
}
