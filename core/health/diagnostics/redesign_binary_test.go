// file: core/health/diagnostics/redesign_binary_test.go
// version: 1.4.0
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

// goTool is the go command the test builds with: the one on PATH, else the one
// under the GOROOT environment variable (runtime.GOROOT is deprecated: it reports
// the root used at build time, which is not meaningful on another machine).
func goTool(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	root := os.Getenv("GOROOT")
	if root == "" {
		t.Fatal("no go command on PATH and GOROOT is not set")
	}
	p := filepath.Join(root, "bin", "go")
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

// TestRedesignRefusal_RealBinary (round 10, N-3; round 11, O-1; round 12, P-1)
// builds a small main that imports this package and runs it as a production
// binary (testing.Testing() is false in it), then checks that diagnostics.New and
// the zero Exporter's Build both return ErrRedesignPendingF8_03b. Every other
// test of the refusal passes the probe in, so a probe that always said "test
// binary" (New passing func() bool { return true }, or testBinary's fallback
// returning true) survived them all; both die here, because the binary reaches
// the probe the way a production caller does.
//
// It does this for each tag set archtest.F803bShippedTagSets derives, so a
// build-tagged copy of the marker, New or Build that the default build never sees
// is run too. What is covered, exactly:
//
//   - the TagSets() entries for this module that run on this host, and every
//     -tags list in Makefile*, Dockerfile*, .github/workflows/ and .woodpecker/,
//     with a make variable in a list ($(GO_BUILD_TAGS)) resolved from the
//     Makefile's own assignments (=, :=, ?=, +=, nested, with an existing
//     -include'd file read at its include line), and the release build,
//     .goreleaser.yml's tags: and env: CGO_ENABLED per goos. The deployed set,
//     `make build-linux`'s embed_frontend fts5 native_taglib pprof, is one of them
//     and is pinned below. A -tags argument the derivation cannot reduce to a
//     concrete list fails archtest.F803bShippedTagSets, so it cannot drop out;
//   - a GOOS other than the host's is not built here (CI builds it on another
//     machine); the gate's constraint rule (b) reads the files of both packages
//     for every GOOS instead.
//
// Still missed: a variable set outside the repo's files (a Makefile.local that
// is not in the checkout, the make command line, the environment, a CI input), a tag list
// a script assembles at run time, a build file outside the scanned set, and
// a linker -X override of the testing package's test-binary variable, which only
// archtest.F803bNoLinkerOverride's text scan guards.
//
// Cost: sets whose probemain build would select the same files in every package
// (go list -deps, same GOOS, CGO and -race) are built once and the rest reported
// as covered by it, so a set is built only when it can differ. Up to two builds
// run at once. The children inherit this process's GOCACHE, so they share
// compiled packages with the parent and each other. Not skipped under -short:
// this is the one test that proves the refusal works in the process it exists
// for.
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
	hostSet := func(ts archtest.TagSet) bool { return ts.GOOS == "" || ts.GOOS == runtime.GOOS }
	var covered []string
	type build struct {
		ts   archtest.TagSet
		same []string // names of the sets whose closure is this one's
	}
	var builds []*build
	byClosure := map[string]*build{}
	for _, ts := range sets {
		if !hostSet(ts) {
			continue
		}
		covered = append(covered, ts.Name)
		sig := closureSignature(t, goBin, root, probePkg, ts)
		if b, ok := byClosure[sig]; ok {
			b.same = append(b.same, ts.Name)
			continue
		}
		b := &build{ts: ts}
		byClosure[sig] = b
		builds = append(builds, b)
	}
	for _, want := range []string{"default", "image"} {
		if !slices.Contains(covered, want) {
			t.Errorf("tag set %q was not covered; covered %v", want, covered)
		}
	}
	if !slices.ContainsFunc(sets, func(ts archtest.TagSet) bool { return slices.Contains(ts.Tags, "embed_frontend") && ts.GOOS == "" }) {
		t.Errorf("no host tag set with embed_frontend, the production image's tag: %v", covered)
	}
	// `make deploy` builds the production binary with this set (through
	// $(GO_BUILD_TAGS) in the Makefile). Losing it from the derivation is the
	// round 11 hole.
	deployed := []string{"embed_frontend", "fts5", "native_taglib", "pprof"}
	if !slices.ContainsFunc(sets, func(ts archtest.TagSet) bool {
		tags := slices.Sorted(slices.Values(ts.Tags))
		return ts.GOOS == "" && slices.Equal(tags, deployed)
	}) {
		t.Errorf("the deployed tag set %v is not among the derived sets %v", deployed, covered)
	}
	t.Logf("%d host tag sets, %d distinct builds", len(covered), len(builds))
	slots := make(chan struct{}, 2)
	for _, b := range builds {
		t.Run(b.ts.Name, func(t *testing.T) {
			t.Parallel()
			slots <- struct{}{}
			defer func() { <-slots }()
			start := time.Now()
			realBinaryRefuses(t, goBin, root, b.ts)
			t.Logf("built and ran in %s; same files as: %v", time.Since(start).Round(100*time.Millisecond), b.same)
		})
	}
}

// probeEnv is the environment a probemain build under ts runs in.
func probeEnv(ts archtest.TagSet) []string {
	env := os.Environ()
	if ts.GOOS != "" {
		env = append(env, "GOOS="+ts.GOOS)
	}
	if ts.CGOEnabled != "" {
		env = append(env, "CGO_ENABLED="+ts.CGOEnabled)
	}
	return env
}

// probeFlags is the build-selection part of the go command line for ts: -race
// when the set names the race tag (which a build may not name with -tags), and
// -tags with the rest.
func probeFlags(ts archtest.TagSet) (flags, tags []string) {
	tags = slices.DeleteFunc(slices.Clone(ts.Tags), func(tag string) bool { return tag == "race" })
	if len(tags) != len(ts.Tags) {
		flags = append(flags, "-race")
	}
	if len(tags) > 0 {
		flags = append(flags, "-tags", strings.Join(tags, ","))
	}
	return flags, tags
}

// probePkg is the main package the real-binary test builds, relative to the
// module root.
const probePkg = "./health/diagnostics/testdata/probemain"

// closureSignature is what a probemain build under ts would compile: every
// package of its dependency closure with the source files the go command selects
// for it under ts's tags, GOOS, CGO setting and -race. Two sets with the same
// signature build the same binary, so one build stands for both. A set whose
// signature is new gets its own build, which is why a tagged copy of Build or the
// marker is built (and refuses) for exactly the set that selects it.
func closureSignature(t *testing.T, goBin, root, pkg string, ts archtest.TagSet) string {
	t.Helper()
	flags, _ := probeFlags(ts)
	args := slices.Concat([]string{"list", "-deps", "-f",
		"{{.ImportPath}} {{.GoFiles}} {{.CgoFiles}} {{.CFiles}} {{.SFiles}} {{.SysoFiles}} {{.EmbedFiles}}"},
		flags, []string{pkg})
	cmd := exec.Command(goBin, args...)
	cmd.Dir = root
	cmd.Env = probeEnv(ts)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("go list -deps under tag set %q (tags %v): %v\n%s", ts.Name, ts.Tags, err, stderr)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// realBinaryRefuses builds testdata/probemain under ts and runs it.
func realBinaryRefuses(t *testing.T, goBin, root string, ts archtest.TagSet) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "probemain")
	// -buildvcs=false: the child build must not stamp VCS, which fails in a
	// container whose checkout git calls dubious.
	flags, tags := probeFlags(ts)
	args := slices.Concat([]string{"build", "-buildvcs=false", "-o", bin}, flags, []string{probePkg})
	build := exec.CommandContext(ctx, goBin, args...)
	build.Dir = root
	build.Env = probeEnv(ts)
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

// TestClosureSignature pins what the dedupe in TestRedesignRefusal_RealBinary
// relies on: two tag sets share a signature only when the go command selects the
// same files in every package of the closure. A set whose tags select a file
// another set does not (an include, an exclude, the race tag, a GOOS suffix, CGO)
// must not be folded into it, or a tagged copy of the refusal would be built for
// no set at all.
func TestClosureSignature(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":         "module probe\n\ngo 1.21\n",
		"main/main.go":   "package main\n\nimport \"probe/p\"\n\nfunc main() { p.F() }\n",
		"p/p.go":         "package p\n\nfunc F() {}\n",
		"p/on.go":        "//go:build shipit\n\npackage p\n\nfunc G() {}\n",
		"p/off.go":       "//go:build !shipit\n\npackage p\n\nfunc H() {}\n",
		"p/both.go":      "//go:build shipit && extra\n\npackage p\n\nfunc I() {}\n",
		"p/race.go":      "//go:build race\n\npackage p\n\nfunc R() {}\n",
		"p/norace.go":    "//go:build !race\n\npackage p\n\nfunc N() {}\n",
		"p/p_windows.go": "package p\n\nfunc W() {}\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goBin := goTool(t)
	sig := func(ts archtest.TagSet) string { return closureSignature(t, goBin, dir, "./main", ts) }
	base := sig(archtest.TagSet{Name: "default", GOOS: runtime.GOOS})
	for name, tc := range map[string]struct {
		ts   archtest.TagSet
		same bool
	}{
		"an unrelated tag":        {archtest.TagSet{Name: "unrelated", GOOS: runtime.GOOS, Tags: []string{"other"}}, true},
		"two unrelated tags":      {archtest.TagSet{Name: "unrelated2", GOOS: runtime.GOOS, Tags: []string{"other", "more"}}, true},
		"a tag that selects":      {archtest.TagSet{Name: "shipit", GOOS: runtime.GOOS, Tags: []string{"shipit"}}, false},
		"a tag that selects, two": {archtest.TagSet{Name: "shipit2", GOOS: runtime.GOOS, Tags: []string{"shipit", "extra"}}, false},
		"only the second tag":     {archtest.TagSet{Name: "extra", GOOS: runtime.GOOS, Tags: []string{"extra"}}, true},
		"race":                    {archtest.TagSet{Name: "race", GOOS: runtime.GOOS, Tags: []string{"race"}}, false},
		"another GOOS":            {archtest.TagSet{Name: "windows", GOOS: "windows"}, false},
	} {
		if got := sig(tc.ts) == base; got != tc.same {
			t.Errorf("%s: same signature as the default build = %v, want %v", name, got, tc.same)
		}
	}
	// The two selecting sets differ from each other too.
	if sig(archtest.TagSet{Name: "a", GOOS: runtime.GOOS, Tags: []string{"shipit"}}) == sig(archtest.TagSet{Name: "b", GOOS: runtime.GOOS, Tags: []string{"shipit", "extra"}}) {
		t.Error("shipit and shipit+extra select different files but share a signature")
	}
}
