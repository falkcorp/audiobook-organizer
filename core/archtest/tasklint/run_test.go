// file: core/archtest/tasklint/run_test.go
// version: 1.0.0
// guid: 59ad832d-2138-4515-ab39-44beaafda01b
// last-edited: 2026-11-10

package tasklint

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// recorder is a testing.TB that records instead of failing, so Run's verdicts
// can be asserted. Fatalf and Skipf record too; Run returns after each.
type recorder struct {
	testing.TB
	errors, fatals, logs, skips []string
}

func (r *recorder) Helper()                   {}
func (r *recorder) Errorf(f string, a ...any) { r.errors = append(r.errors, fmt.Sprintf(f, a...)) }
func (r *recorder) Fatalf(f string, a ...any) { r.fatals = append(r.fatals, fmt.Sprintf(f, a...)) }
func (r *recorder) Skipf(f string, a ...any)  { r.skips = append(r.skips, fmt.Sprintf(f, a...)) }
func (r *recorder) Logf(f string, a ...any)   { r.logs = append(r.logs, fmt.Sprintf(f, a...)) }
func (r *recorder) Log(a ...any)              { r.logs = append(r.logs, fmt.Sprint(a...)) }
func (r *recorder) count(sub string, in []string) int {
	n := 0
	for _, s := range in {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// fixtureGOPATH points the go command at the analysistest tree (GOPATH mode)
// and returns its src directory, the Root of the fixture runs.
func fixtureGOPATH(t *testing.T) string {
	t.Helper()
	td := analysistest.TestData()
	t.Setenv("GOPATH", td)
	t.Setenv("GO111MODULE", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	src := filepath.Join(td, "src")
	t.Chdir(src)
	return src
}

func writeEntries(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, text := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const writerFile = "github.com/falkcorp/mediacore/internal/tasklintcases/writer_bypass/writer.go"

// TestRun_ExemptionUnion: writer-bypass findings fail Run unless their file is
// in the union; the union never exempts another rule; missing-help is logged;
// a stale or duplicated line fails.
func TestRun_ExemptionUnion(t *testing.T) {
	root := fixtureGOPATH(t)
	base := Options{ModulePath: "github.com/falkcorp/mediacore", StorePackages: []string{caseStore}, Root: root}
	run := func(pkgs []string, exempt map[string]string) *recorder {
		o := base
		o.Packages = pkgs
		if exempt != nil {
			o.ExemptDir = writeEntries(t, exempt)
		}
		r := &recorder{TB: t}
		Run(r, o)
		return r
	}

	r := run([]string{cases + "writer_bypass"}, nil)
	if len(r.fatals) != 0 || r.count("[writer-bypass]", r.errors) != 3 {
		t.Fatalf("no exemptions: fatals %v, errors %v; want 3 writer-bypass errors", r.fatals, r.errors)
	}

	r = run([]string{cases + "writer_bypass"}, map[string]string{"F5-99.txt": "# owned by F5-99\n" + writerFile + "\n"})
	if len(r.errors)+len(r.fatals) != 0 || r.count("3 exempt", r.logs) != 1 {
		t.Fatalf("file exempt: errors %v fatals %v logs %v; want none and 3 exempt", r.errors, r.fatals, r.logs)
	}

	seq := "github.com/falkcorp/mediacore/internal/tasklintcases/sequential_empty_reason/seq.go"
	r = run([]string{cases + "sequential_empty_reason"}, map[string]string{"F5-99.txt": seq + "\n"})
	if r.count("[sequential-empty-reason]", r.errors) != 2 {
		t.Fatalf("the union exempted another rule: errors %v", r.errors)
	}

	r = run([]string{cases + "missing_help"}, nil)
	if len(r.errors) != 0 || r.count("[missing-help]", r.logs) != 3 {
		t.Fatalf("missing-help: errors %v logs %v; want 3 logged warnings and no error", r.errors, r.logs)
	}

	r = run([]string{cases + "clean"}, map[string]string{
		"A.txt": "github.com/falkcorp/mediacore/internal/tasklintcases/clean/gone.go\n" + writerFile + "\n",
		"B.txt": writerFile + "\n",
	})
	if r.count("stale exemption", r.errors) != 1 || r.count("already listed by A.txt", r.errors) != 1 {
		t.Fatalf("stale and duplicate lines: errors %v", r.errors)
	}
}

// TestRun_Refusals: a missing module path, an unloadable pattern and an
// unreadable exemption directory each stop Run.
func TestRun_Refusals(t *testing.T) {
	root := fixtureGOPATH(t)
	for name, o := range map[string]Options{
		"no module":     {Packages: []string{cases + "clean"}},
		"no packages":   {ModulePath: "m"},
		"bad pattern":   {ModulePath: "github.com/falkcorp/mediacore", Packages: []string{cases + "does_not_exist"}, Root: root},
		"no exempt dir": {ModulePath: "github.com/falkcorp/mediacore", Packages: []string{cases + "clean"}, Root: root, ExemptDir: filepath.Join(t.TempDir(), "absent")},
	} {
		r := &recorder{TB: t}
		Run(r, o)
		if len(r.fatals) != 1 {
			t.Errorf("%s: fatals %v, want one", name, r.fatals)
		}
	}
}

func TestParseEntry(t *testing.T) {
	lines, problems := ParseEntry("# F5-01 owns this file\n\ninternal/a.go\n  internal/b.go  \n/abs.go\n../up.go\ninternal//c.go\nwin\\d.go\n.\n")
	if strings.Join(lines, ",") != "internal/a.go,internal/b.go" {
		t.Errorf("lines = %v", lines)
	}
	if len(problems) != 5 {
		t.Errorf("problems = %v, want 5 (absolute, parent, unclean, backslash, dot)", problems)
	}
}

func TestReadUnion(t *testing.T) {
	dir := writeEntries(t, map[string]string{"F5-01.txt": "a.go\nb.go\n", "F5-02.txt": "c.go\nbad/../x.go\n"})
	u, problems, err := ReadUnion(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(u) != 3 || u["c.go"] != "F5-02.txt" || len(problems) != 1 {
		t.Errorf("union %v problems %v", u, problems)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadUnion(dir); err == nil {
		t.Error("a sub-directory in the exemption directory was accepted")
	}
}

// TestShrinkViolations: an added file or line fails; a removed file or line,
// a comment change and the directory's creation pass.
func TestShrinkViolations(t *testing.T) {
	base := map[string]string{"F5-01.txt": "# owner F5-01\na.go\nb.go\n", "F5-02.txt": "c.go\n"}
	for name, tc := range map[string]struct {
		base, head map[string]string
		want       int
	}{
		"unchanged":     {base, base, 0},
		"line removed":  {base, map[string]string{"F5-01.txt": "a.go\n", "F5-02.txt": "c.go\n"}, 0},
		"file removed":  {base, map[string]string{"F5-01.txt": "a.go\nb.go\n"}, 0},
		"comment edit":  {base, map[string]string{"F5-01.txt": "# new comment\na.go\nb.go\n", "F5-02.txt": "c.go\n"}, 0},
		"created":       {nil, map[string]string{"F5-01.txt": "a.go\n"}, 0},
		"line added":    {base, map[string]string{"F5-01.txt": "a.go\nb.go\nd.go\n", "F5-02.txt": "c.go\n"}, 1},
		"line moved":    {base, map[string]string{"F5-01.txt": "a.go\n", "F5-02.txt": "b.go\nc.go\n"}, 1},
		"file added":    {base, map[string]string{"F5-01.txt": "a.go\nb.go\n", "F5-02.txt": "c.go\n", "F5-77.txt": "e.go\n"}, 1},
		"emptied, kept": {base, map[string]string{"F5-01.txt": "", "F5-02.txt": ""}, 0},
	} {
		if got := ShrinkViolations(tc.base, tc.head); len(got) != tc.want {
			t.Errorf("%s: %d violations %v, want %d", name, len(got), got, tc.want)
		}
	}
}

// TestCheckShrinkOnly_Git runs the git half in a scratch repository whose
// origin/main is the base: a removal passes, an addition fails, and a
// checkout without origin/main skips locally and fails under CI=true.
func TestCheckShrinkOnly_Git(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Lucius Fox", "-c", "user.email=lucius@example.com",
			"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ex := filepath.Join(repo, "app", "testdata", "exempt.d")
	write := func(name, text string) {
		t.Helper()
		if err := os.MkdirAll(ex, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ex, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "work")
	write("README", "")
	if err := os.Remove(filepath.Join(ex, "README")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "base without the directory")
	t.Chdir(filepath.Join(repo))
	t.Setenv("CI", "")

	// No origin/main: skip locally, fail under CI.
	r := &recorder{TB: t}
	CheckShrinkOnly(r, "app/testdata/exempt.d")
	if len(r.skips) != 1 {
		t.Fatalf("without origin/main: skips %v fatals %v", r.skips, r.fatals)
	}
	t.Setenv("CI", "true")
	r = &recorder{TB: t}
	CheckShrinkOnly(r, "app/testdata/exempt.d")
	if len(r.fatals) != 1 {
		t.Fatalf("without origin/main under CI: fatals %v", r.fatals)
	}

	// The base has no directory: creating it passes.
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	write("F5-01.txt", "a.go\nb.go\n")
	r = &recorder{TB: t}
	CheckShrinkOnly(r, "app/testdata/exempt.d")
	if len(r.errors)+len(r.fatals)+len(r.skips) != 0 {
		t.Fatalf("creation: errors %v fatals %v skips %v", r.errors, r.fatals, r.skips)
	}

	// Now the base holds it; work from app/ to exercise the prefix.
	git("add", "-A")
	git("commit", "-qm", "create")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	t.Chdir(filepath.Join(repo, "app"))
	write("F5-01.txt", "a.go\n")
	r = &recorder{TB: t}
	CheckShrinkOnly(r, "testdata/exempt.d")
	if len(r.errors)+len(r.fatals) != 0 {
		t.Fatalf("shrink: errors %v fatals %v", r.errors, r.fatals)
	}
	write("F5-01.txt", "a.go\nb.go\nc.go\n")
	write("F5-02.txt", "d.go\n")
	r = &recorder{TB: t}
	CheckShrinkOnly(r, "testdata/exempt.d")
	if len(r.errors) != 2 {
		t.Fatalf("growth: errors %v, want 2 (a line and a file)", r.errors)
	}
	r = &recorder{TB: t}
	CheckShrinkOnly(r, "testdata/absent.d")
	if len(r.fatals) != 1 {
		t.Fatalf("absent directory: fatals %v", r.fatals)
	}
}
