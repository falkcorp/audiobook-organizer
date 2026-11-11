// file: core/archtest/guards_test.go
// version: 1.0.0
// guid: 72447f4d-bf7d-4e3d-8cb9-1efecea9b890
// last-edited: 2026-11-10

package archtest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureWants reads the "// want: <substring>" markers of a fixture
// directory (and "// want-above: <substring>" for the line above) as
// file:line -> substrings.
func fixtureWants(t *testing.T, dir string) map[string][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixture %s: %v (%d files)", dir, err, len(files))
	}
	out := map[string][]string{}
	for _, f := range files {
		abs, _ := filepath.Abs(f)
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			for marker, at := range map[string]int{"// want-above: ": n - 1, "// want: ": n} {
				if _, after, ok := strings.Cut(line, marker); ok {
					key := fmt.Sprintf("%s:%d", abs, at)
					out[key] = append(out[key], strings.TrimSpace(after))
				}
			}
		}
		_ = fh.Close()
	}
	return out
}

// checkWants asserts that findings and the fixture's markers match one to one.
func checkWants(t *testing.T, dir string, findings []GuardFinding) {
	t.Helper()
	wants := fixtureWants(t, dir)
	for _, f := range findings {
		key := fmt.Sprintf("%s:%d", f.File, f.Line)
		matched := -1
		for i, w := range wants[key] {
			if strings.Contains(f.Msg, w) {
				matched = i
				break
			}
		}
		if matched < 0 {
			t.Errorf("unexpected finding %s", f)
			continue
		}
		wants[key] = append(wants[key][:matched], wants[key][matched+1:]...)
	}
	for key, ws := range wants {
		for _, w := range ws {
			t.Errorf("%s: no finding matching %q", key, w)
		}
	}
}

func guardFixture(name string) string {
	return "./" + filepath.ToSlash(filepath.Join("testdata", "guards", name))
}

// TestResumeContinueCheckpoints_SelfTest: one passing and one failing case per
// clause of 05-tasks §5T.3.6 (a direct checkpoint, one reached through
// same-package functions and a method value, a Watermark loop, a reviewed
// from-zero directive; no checkpoint, a loop without a Watermark, an
// unreadable Common or spec, a directive without a reason, a stale directive).
func TestResumeContinueCheckpoints_SelfTest(t *testing.T) {
	for _, c := range []string{"resume/pass", "resume/fail"} {
		t.Run(c, func(t *testing.T) {
			f, err := resumeContinueFindings([]string{guardFixture(c)})
			if err != nil {
				t.Fatal(err)
			}
			checkWants(t, filepath.Join("testdata", "guards", c), f)
		})
	}
	if _, err := resumeContinueFindings(nil); err == nil {
		t.Error("no pattern was accepted")
	}
}

// TestRunItemsDeclareParallelism_SelfTest: each accepted clause of 05-tasks
// §5T.3.2 passes (constructors, a variable, a helper's returns, a parameter
// over every call site, FromParam) and each refusal is flagged (a zero var, a
// zero literal assignment, an unproven parameter, an unrequired options field,
// blank reasons, a BatchSpec without Parallelism).
func TestRunItemsDeclareParallelism_SelfTest(t *testing.T) {
	for _, c := range []string{"par/pass", "par/fail"} {
		t.Run(c, func(t *testing.T) {
			f, err := parallelismFindings([]string{guardFixture(c)})
			if err != nil {
				t.Fatal(err)
			}
			checkWants(t, filepath.Join("testdata", "guards", c), f)
		})
	}
	if _, err := parallelismFindings([]string{"./testdata/guards/does-not-exist"}); err == nil {
		t.Error("an unloadable pattern was accepted")
	}
}

// TestArchtest_PassThroughParallelismAccepted is 05-tasks §5T.9's R7
// pass-through row: a tasks.Parallelism-typed field (clause b) and a validated
// //mediacore:required options field (clause c) are accepted (COR-23).
func TestArchtest_PassThroughParallelismAccepted(t *testing.T) {
	f, err := parallelismFindings([]string{guardFixture("par/passthrough")})
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 0 {
		t.Errorf("pass-through flagged: %v", f)
	}
}

// TestArchtest_ZeroLiteralFlagged is the row's other half: a
// tasks.Parallelism{} literal, a zero var, Sequential("") and Fixed(n, "") are
// flagged, and so is a required field nobody validates.
func TestArchtest_ZeroLiteralFlagged(t *testing.T) {
	f, err := parallelismFindings([]string{guardFixture("par/zero")})
	if err != nil {
		t.Fatal(err)
	}
	checkWants(t, filepath.Join("testdata", "guards", "par", "zero"), f)
}

// TestGuards_Wrappers: the exported guards fail t on a finding and pass a
// clean package.
func TestGuards_Wrappers(t *testing.T) {
	r := &guardRecorder{TB: t}
	RunItemsDeclareParallelism(r, guardFixture("par/zero"))
	ResumeContinueCheckpoints(r, guardFixture("resume/fail"))
	if r.errors == 0 || r.fatals != 0 {
		t.Errorf("errors %d fatals %d on failing fixtures", r.errors, r.fatals)
	}
	r = &guardRecorder{TB: t}
	RunItemsDeclareParallelism(r, guardFixture("par/pass"))
	ResumeContinueCheckpoints(r, guardFixture("resume/pass"))
	if r.errors+r.fatals != 0 {
		t.Errorf("errors %d fatals %d on passing fixtures", r.errors, r.fatals)
	}
	r = &guardRecorder{TB: t}
	RunItemsDeclareParallelism(r)
	ResumeContinueCheckpoints(r)
	if r.fatals != 2 {
		t.Errorf("fatals %d with no pattern, want 2", r.fatals)
	}
}

type guardRecorder struct {
	testing.TB
	errors, fatals int
}

func (r *guardRecorder) Helper()               {}
func (r *guardRecorder) Errorf(string, ...any) { r.errors++ }
func (r *guardRecorder) Fatalf(string, ...any) { r.fatals++ }
