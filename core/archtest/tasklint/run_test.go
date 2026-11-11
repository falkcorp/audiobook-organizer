// file: core/archtest/tasklint/run_test.go
// version: 1.2.0
// guid: 59ad832d-2138-4515-ab39-44beaafda01b
// last-edited: 2026-11-10

package tasklint

import (
	"fmt"
	"os"
	"os/exec"
	"path"
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
	if len(r.errors) != 0 || r.count("[missing-help]", r.logs) != 4 {
		t.Fatalf("missing-help: errors %v logs %v; want 4 logged warnings and no error", r.errors, r.logs)
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
		"unchanged":        {base, base, 0},
		"line removed":     {base, map[string]string{"F5-01.txt": "a.go\n", "F5-02.txt": "c.go\n"}, 0},
		"file removed":     {base, map[string]string{"F5-01.txt": "a.go\nb.go\n"}, 0},
		"comment edit":     {base, map[string]string{"F5-01.txt": "# new comment\na.go\nb.go\n", "F5-02.txt": "c.go\n"}, 0},
		"created":          {nil, map[string]string{"F5-01.txt": "a.go\n"}, 0},
		"line added":       {base, map[string]string{"F5-01.txt": "a.go\nb.go\nd.go\n", "F5-02.txt": "c.go\n"}, 1},
		"line moved":       {base, map[string]string{"F5-01.txt": "a.go\n", "F5-02.txt": "b.go\nc.go\n"}, 1},
		"file added":       {base, map[string]string{"F5-01.txt": "a.go\nb.go\n", "F5-02.txt": "c.go\n", "F5-77.txt": "e.go\n"}, 1},
		"empty file added": {base, map[string]string{"F5-01.txt": "a.go\nb.go\n", "F5-02.txt": "c.go\n", "F5-78.txt": "# owner F5-78\n"}, 1},
		"emptied, kept":    {base, map[string]string{"F5-01.txt": "", "F5-02.txt": ""}, 0},
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
	t.Setenv(ShrinkBaseEnv, "") // CI sets it for the real checkout, not this one
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

// TestCheckShrinkOnly_ShallowCI reproduces CI's checkout: the PR's merge ref
// at depth 1, then `git fetch --no-tags --depth=1 origin main`. No merge base
// is reachable across the shallow boundary, so origin/main is the base. Before
// the fetch there is no origin/main and CI fails; after it a removal passes and
// an addition fails.
func TestCheckShrinkOnly_ShallowCI(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv(ShrinkBaseEnv, "")
	root := t.TempDir()
	up, co := filepath.Join(root, "up"), filepath.Join(root, "co")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Lucius Fox", "-c", "user.email=lucius@example.com",
			"-c", "commit.gpgsign=false", "-c", "protocol.file.allow=always"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ex := filepath.Join("app", "testdata", "exempt.d")
	write := func(name, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(up, ex), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(up, ex, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(up, 0o755); err != nil {
		t.Fatal(err)
	}
	git(up, "init", "-q", "-b", "main")
	write("F5-01.txt", "a.go\nb.go\n")
	git(up, "add", "-A")
	git(up, "commit", "-qm", "base")
	write("F5-02.txt", "c.go\n")
	git(up, "add", "-A")
	git(up, "commit", "-qm", "main moves on")
	// Two PRs branched from the first commit, each a merge ref onto main.
	pr := func(n, text string) {
		t.Helper()
		git(up, "checkout", "-q", "-b", "pr"+n, "HEAD~1")
		write("F5-01.txt", text)
		git(up, "commit", "-qam", "pr "+n)
		git(up, "checkout", "-q", "--detach", "main")
		git(up, "merge", "-q", "--no-ff", "pr"+n, "-m", "merge "+n)
		git(up, "update-ref", "refs/pull/"+n+"/merge", "HEAD")
		git(up, "checkout", "-q", "main")
	}
	pr("1", "a.go\n")
	pr("2", "a.go\nb.go\nd.go\n")

	// explicit does what ci.yml does: read the merge ref's first parent from
	// the raw commit (the shallow boundary hides it from rev-parse), fetch it
	// at depth 1 and pass it through ShrinkBaseEnv.
	check := func(n string, fetchMain, explicit bool) *recorder {
		t.Helper()
		if err := os.RemoveAll(co); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(co, 0o755); err != nil {
			t.Fatal(err)
		}
		git(co, "init", "-q")
		git(co, "remote", "add", "origin", "file://"+filepath.ToSlash(up))
		git(co, "fetch", "-q", "--no-tags", "--depth=1", "origin", "+refs/pull/"+n+"/merge:refs/remotes/pull/"+n+"/merge")
		git(co, "checkout", "-q", "--detach", "refs/remotes/pull/"+n+"/merge")
		if fetchMain {
			git(co, "fetch", "-q", "--no-tags", "--depth=1", "origin", "main")
		}
		t.Setenv(ShrinkBaseEnv, "")
		if explicit {
			cmd := exec.Command("git", "cat-file", "-p", "HEAD")
			cmd.Dir = co
			raw, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var parent string
			for l := range strings.SplitSeq(string(raw), "\n") {
				if p, ok := strings.CutPrefix(l, "parent "); ok {
					parent = p
					break
				}
			}
			git(co, "fetch", "-q", "--no-tags", "--depth=1", "origin", parent)
			t.Setenv(ShrinkBaseEnv, parent)
		}
		t.Chdir(co)
		t.Setenv("CI", "true")
		r := &recorder{TB: t}
		CheckShrinkOnly(r, ex)
		return r
	}
	if r := check("1", false, false); len(r.fatals) != 1 {
		t.Fatalf("shallow without origin/main: fatals %v errors %v", r.fatals, r.errors)
	}
	if r := check("1", true, false); len(r.errors)+len(r.fatals)+len(r.skips) != 0 {
		t.Fatalf("shallow shrink: errors %v fatals %v skips %v", r.errors, r.fatals, r.skips)
	}
	if r := check("2", true, false); len(r.fatals) != 0 || r.count("gained d.go", r.errors) != 1 || len(r.errors) != 1 {
		t.Fatalf("shallow growth: errors %v fatals %v, want one gained d.go", r.errors, r.fatals)
	}

	// Main moves on after the merge refs were made, dropping c.go. Against
	// origin/main (the fallback) PR 1 now reads as gaining c.go, and rerunning
	// would not change that. The explicit base is the commit the merge ref was
	// built on, so it is exact.
	write("F5-02.txt", "")
	git(up, "commit", "-qam", "main drops c.go")
	if r := check("1", true, false); len(r.errors) != 1 || !strings.Contains(r.errors[0], "gained c.go") {
		t.Fatalf("fallback after main moved: errors %v fatals %v, want the known false gain of c.go", r.errors, r.fatals)
	}
	if r := check("1", true, true); len(r.errors)+len(r.fatals) != 0 {
		t.Fatalf("explicit base after main moved: errors %v fatals %v", r.errors, r.fatals)
	}
	if r := check("2", false, true); len(r.fatals) != 0 || r.count("gained d.go", r.errors) != 1 || len(r.errors) != 1 {
		t.Fatalf("explicit base, growth: errors %v fatals %v, want one gained d.go", r.errors, r.fatals)
	}
}

// TestAnalyze_WholeProgram: unregistered-kind is checked over every loaded
// package at once. The kernel registering the owner's kinds (by var, through
// a list it calls, through a helper and a method) leaves the owner clean; the
// stray package's kinds, one built through the owner's helper, are reported
// because no loaded package registers them. Loaded alone, the owner's kinds
// are reported, and with ListFuncs a list function counts only once a loaded
// package calls it.
func TestAnalyze_WholeProgram(t *testing.T) {
	root := fixtureGOPATH(t)
	x := cases + "xpkg/"
	run := func(listFuncs bool, pkgs ...string) []string {
		t.Helper()
		cfg := fixtureConfig()
		cfg.ListFuncs = listFuncs
		cfg.Constructors = nil
		var full []string
		for _, p := range pkgs {
			full = append(full, x+p)
		}
		fs, _, err := Analyze(cfg, root, full)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range fs {
			if f.Category == CatUnregisteredKind {
				out = append(out, fmt.Sprintf("%s:%d %s", path.Base(f.File), f.Line, f.Message))
			}
		}
		return out
	}
	got := run(false, "owner", "kernel", "stray")
	if len(got) != 2 || !strings.HasPrefix(got[0], "stray.go:15 tasks.Task definition in Forgotten is not registered: no Registrar call in any loaded package") ||
		!strings.HasPrefix(got[1], "stray.go:18 definition built by Build in Lost is not registered") {
		t.Errorf("owner+kernel+stray: %q, want Forgotten and Lost only", got)
	}
	if got := run(false, "owner"); len(got) != 3 {
		t.Errorf("owner alone: %q, want its 3 kind sites (Single, and the Build and Batch.Def calls in Kinds; the helpers have callers, so their own constructors are not sites)", got)
	}
	if got := run(true, "listowner"); len(got) != 1 || !strings.Contains(got[0], "no called list function") {
		t.Errorf("uncalled list function: %q, want its kind reported", got)
	}
	if got := run(true, "listowner", "wire"); len(got) != 0 {
		t.Errorf("list function called by wire: %q, want none", got)
	}
}

// TestCheckShrinkOnly_BaseAndGenesis covers the base choice and the genesis
// rule:
//   - HEAD older than main: main dropped a line after HEAD branched. The merge
//     base sees no growth, and an explicit ShrinkBaseEnv is used as given.
//   - An unknown ShrinkBaseEnv commit fails under CI.
//   - A directory recreated after its deletion fails.
//   - A directory created after the package's tests already call
//     CheckShrinkOnly fails.
func TestCheckShrinkOnly_BaseAndGenesis(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	out := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Lucius Fox", "-c", "user.email=lucius@example.com",
			"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	put := func(name, text string) {
		t.Helper()
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) string {
		t.Helper()
		out("add", "-A")
		out("commit", "-qm", msg)
		return out("rev-parse", "HEAD")
	}
	check := func() *recorder {
		t.Helper()
		r := &recorder{TB: t}
		CheckShrinkOnly(r, "exempt.d")
		return r
	}
	t.Chdir(repo)
	t.Setenv("CI", "true")
	t.Setenv(ShrinkBaseEnv, "")

	out("init", "-q", "-b", "main")
	put("exempt.d/F5-01.txt", "a.go\nb.go\n")
	put("shrink_test.go", "package app\n\n// calls tasklint.CheckShrinkOnly(t, \"exempt.d\")\n")
	branchPoint := commit("ratchet and list")
	put("exempt.d/F5-01.txt", "a.go\n")
	mainTip := commit("main drops b.go")
	out("update-ref", "refs/remotes/origin/main", mainTip)
	out("checkout", "-q", "-b", "older", branchPoint)

	if r := check(); len(r.errors)+len(r.fatals)+len(r.skips) != 0 {
		t.Fatalf("HEAD older than main, merge base: errors %v fatals %v skips %v", r.errors, r.fatals, r.skips)
	}
	t.Setenv(ShrinkBaseEnv, branchPoint)
	if r := check(); len(r.errors)+len(r.fatals) != 0 {
		t.Fatalf("explicit base at the branch point: errors %v fatals %v", r.errors, r.fatals)
	}
	t.Setenv(ShrinkBaseEnv, mainTip)
	if r := check(); len(r.errors) != 1 || !strings.Contains(r.errors[0], "gained b.go") {
		t.Fatalf("explicit base at main's tip is used as given: errors %v fatals %v", r.errors, r.fatals)
	}
	t.Setenv(ShrinkBaseEnv, "0123456789abcdef0123456789abcdef01234567")
	if r := check(); len(r.fatals) != 1 || !strings.Contains(r.fatals[0], ShrinkBaseEnv) {
		t.Fatalf("unknown explicit base: fatals %v", r.fatals)
	}
	t.Setenv(ShrinkBaseEnv, "")

	// Deleted on main, then recreated: no genesis.
	out("checkout", "-q", "main")
	out("rm", "-rq", "exempt.d")
	deleted := commit("list emptied and deleted")
	out("update-ref", "refs/remotes/origin/main", deleted)
	put("exempt.d/F5-01.txt", "a.go\n")
	if r := check(); len(r.errors) != 1 || !strings.Contains(r.errors[0], "entry file F5-01.txt was added") {
		t.Fatalf("recreated directory: errors %v fatals %v", r.errors, r.fatals)
	}
	// The ratchet's call went too: the history alone still refuses it.
	if err := os.RemoveAll(filepath.Join(repo, "exempt.d")); err != nil {
		t.Fatal(err)
	}
	out("rm", "-q", "shrink_test.go")
	out("update-ref", "refs/remotes/origin/main", commit("ratchet removed"))
	put("exempt.d/F5-01.txt", "a.go\n")
	if r := check(); len(r.errors) != 1 || !strings.Contains(r.errors[0], "entry file F5-01.txt was added") {
		t.Fatalf("recreated directory without the ratchet at the base: errors %v fatals %v", r.errors, r.fatals)
	}
	put("shrink_test.go", "package app\n\n// calls tasklint.CheckShrinkOnly(t, \"exempt.d\")\n")

	// A fresh history where the ratchet came first and the directory later.
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(repo, "exempt.d")); err != nil {
		t.Fatal(err)
	}
	out("init", "-q", "-b", "main")
	ratchet := commit("ratchet without a list")
	out("update-ref", "refs/remotes/origin/main", ratchet)
	put("exempt.d/F5-01.txt", "a.go\n")
	if r := check(); len(r.errors) != 1 || !strings.Contains(r.errors[0], "was added") {
		t.Fatalf("directory created after the ratchet: errors %v fatals %v", r.errors, r.fatals)
	}
	// A history with neither the ratchet nor the list: adding both is the
	// genesis.
	for _, p := range []string{".git", "exempt.d", "shrink_test.go"} {
		if err := os.RemoveAll(filepath.Join(repo, p)); err != nil {
			t.Fatal(err)
		}
	}
	out("init", "-q", "-b", "main")
	put("README", "Wayne Enterprises\n")
	out("update-ref", "refs/remotes/origin/main", commit("before the ratchet"))
	put("exempt.d/F5-01.txt", "a.go\n")
	put("shrink_test.go", "package app\n\n// calls tasklint.CheckShrinkOnly(t, \"exempt.d\")\n")
	if r := check(); len(r.errors)+len(r.fatals) != 0 {
		t.Fatalf("genesis: errors %v fatals %v", r.errors, r.fatals)
	}
}

// TestAnalyze_ConstructorAllowlist: the whole-program pass reports a
// Constructors entry that no loaded function carrying the directive matches,
// and accepts the listed one.
func TestAnalyze_ConstructorAllowlist(t *testing.T) {
	root := fixtureGOPATH(t)
	cfg := fixtureConfig()
	listed := cases + "unregistered_kind.NewDerived"
	for _, tc := range []struct {
		list  []string
		stale int
	}{
		{[]string{listed}, 0},
		{[]string{listed, cases + "unregistered_kind.Gone"}, 1},
	} {
		cfg.Constructors = tc.list
		fs, _, err := Analyze(cfg, root, []string{cases + "unregistered_kind"})
		if err != nil {
			t.Fatal(err)
		}
		stale, refused := 0, 0
		for _, f := range fs {
			if f.File == "(Options)" && strings.Contains(f.Message, "unregistered_kind.Gone, which no loaded function") {
				stale++
			}
			if strings.Contains(f.Message, "on NewDerived, which Config.Constructors") {
				refused++
			}
		}
		if stale != tc.stale || refused != 0 {
			t.Errorf("list %v: %d stale (want %d), NewDerived refused %d times; findings %v", tc.list, stale, tc.stale, refused, fs)
		}
	}
}
