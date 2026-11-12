// file: internal/maintenance/jobs/writes_oracle_test.go
// version: 1.0.0
// guid: 40eadd67-76ac-419f-a3fe-7d1ed5e586e2
// last-edited: 2026-11-12

package jobs_test

import (
	"fmt"
	"go/constant"
	"go/types"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/mediacore/tasks"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// oracleModule is the module whose code the oracle walks into; a call that
// leaves it is judged by name only (os file mutators), never followed.
const oracleModule = "github.com/falkcorp/audiobook-organizer"

// oracleVerb is what makes an interface method call a write. A method that
// does not start with one of these is a read.
var oracleVerb = regexp.MustCompile(`^(Create|Update|Delete|Set|Save|Add|Remove|Put|Modify|Move|Reassign|Prune|Recompute|Upsert|Write|Mark|Batch|Reset|Purge|Insert|Apply|Retire|Clear|Replace|Merge|Rename|Link|Unlink|Store|Record|Append|Touch|Increment|Mint|Sweep)`)

// oracleNotAWrite names receivers whose "writes" are not a books resource:
// progress and liveness reporting, hashing, and the activity log's own sink.
var oracleNotAWrite = regexp.MustCompile(`Reporter|LivenessToucher|hash\.Hash|ActivityLogWriter`)

// oracleFileWriters are the os functions that change a file or directory.
var oracleFileWriters = map[string]bool{
	"Remove": true, "RemoveAll": true, "Rename": true, "WriteFile": true, "Chmod": true,
	"Chown": true, "Chtimes": true, "Mkdir": true, "MkdirAll": true, "Create": true,
	"OpenFile": true, "Truncate": true, "Symlink": true, "Link": true,
}

// oracleResource names the resource kind an interface write touches, from the
// receiver's type name and the method name alone. It is deliberately not the
// table's vocabulary applied to a job: it reads what the code calls.
func oracleResource(recv, method string) string {
	if r := oracleNoun(method); r != "" {
		return r
	}
	if r := oracleNoun(recv); r != "" {
		return r
	}
	return "UNCLASSIFIED(" + recv + "." + method + ")"
}

// oracleNoun is the resource a name speaks of, or "" when it names none. The
// method is asked before the receiver, so ModifyBook on a type called
// tagAuthorStore is still a book write.
func oracleNoun(name string) string {
	switch {
	case strings.Contains(name, "Sync"):
		return "sync_identity"
	case strings.Contains(name, "Metric"):
		return "metrics"
	case strings.Contains(name, "Setting"):
		return "settings"
	case strings.Contains(name, "ExternalID"):
		return "external_ids"
	case strings.Contains(name, "Operation"), strings.Contains(name, "checkpointer"),
		strings.Contains(name, "Journal"), strings.Contains(name, "pendingRepair"), strings.Contains(name, "retentionKV"):
		return "operations"
	case strings.Contains(name, "UserBookState"), strings.Contains(name, "UserPosition"),
		strings.Contains(name, "UserTag"), strings.Contains(name, "Bookmark"), strings.Contains(name, "UserProgress"):
		return "user_state"
	case strings.Contains(name, "BookFile"), strings.Contains(name, "FileEvent"):
		return "book_files"
	case strings.Contains(name, "Author"):
		return "authors"
	case strings.Contains(name, "Series"):
		return "series"
	case strings.Contains(name, "Book"):
		return "books"
	}
	return ""
}

// derivedWrites walks every function a job's Run reaches (closures included,
// inside the module only) and returns the resource kinds it writes.
func derivedWrites(prog *ssa.Program, runFn *ssa.Function) (kinds []string, evidence map[string]string, checkpoints bool) {
	set := map[string]bool{}
	evidence = map[string]string{}
	note := func(kind, why string) {
		set[kind] = true
		if _, ok := evidence[kind]; !ok {
			evidence[kind] = why
		}
	}
	seen := map[*ssa.Function]bool{}
	var walk func(f *ssa.Function)
	walk = func(f *ssa.Function) {
		if f == nil || seen[f] {
			return
		}
		seen[f] = true
		// A synthetic wrapper (a promoted method, a bound method value) has no
		// package; it is followed, because it only forwards.
		if f.Pkg != nil {
			path := f.Pkg.Pkg.Path()
			if !strings.HasPrefix(path, oracleModule) || strings.Contains(path, "/internal/books/store") {
				return
			}
		}
		for _, af := range f.AnonFuncs {
			walk(af)
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if mc, ok := in.(*ssa.MakeClosure); ok {
					if fn, ok := mc.Fn.(*ssa.Function); ok {
						walk(fn)
					}
				}
				// A package-level func variable (hbOpenFile and its kind) is
				// assigned in the package's init; a call through it has no
				// static callee, so reading one follows the functions init stores into that variable.
				if u, ok := in.(*ssa.UnOp); ok {
					if g, ok := u.X.(*ssa.Global); ok {
						if _, isFn := g.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Signature); isFn && g.Pkg != nil {
							if initFn := g.Pkg.Func("init"); initFn != nil {
								for _, ib := range initFn.Blocks {
									for _, ii := range ib.Instrs {
										st, ok := ii.(*ssa.Store)
										if !ok || st.Addr != g {
											continue
										}
										switch v := st.Val.(type) {
										case *ssa.Function:
											walk(v)
										case *ssa.MakeClosure:
											if fn, ok := v.Fn.(*ssa.Function); ok {
												walk(fn)
											}
										}
									}
								}
							}
						}
					}
				}
				ci, ok := in.(ssa.CallInstruction)
				if !ok {
					continue
				}
				com := ci.Common()
				if com.IsInvoke() {
					name := com.Method.Name()
					recv := types.TypeString(com.Value.Type(), func(p *types.Package) string { return p.Name() })
					if oracleVerb.MatchString(name) && !oracleNotAWrite.MatchString(recv) {
						note(oracleResource(recv, name), recv+"."+name+" in "+f.String())
					}
					continue
				}
				callee := com.StaticCallee()
				if callee == nil {
					continue
				}
				// A job restores a checkpoint only through checkpointerFrom (or
				// the v1 helpers it falls back to when that returns nil).
				if callee.Name() == "checkpointerFrom" {
					checkpoints = true
				}
				if callee.Pkg != nil && callee.Pkg.Pkg.Path() == "os" && oracleFileWriters[callee.Name()] {
					note("files", "os."+callee.Name()+" in "+f.String())
				}
				// A concrete store type called directly (not through an
				// interface) is judged like an interface call: the store
				// package's own bodies are not walked.
				if callee.Pkg != nil && strings.Contains(callee.Pkg.Pkg.Path(), "/internal/books/store") && callee.Signature.Recv() != nil && oracleVerb.MatchString(callee.Name()) {
					recv := types.TypeString(callee.Signature.Recv().Type(), func(p *types.Package) string { return p.Name() })
					if !oracleNotAWrite.MatchString(recv) {
						note(oracleResource(recv, callee.Name()), recv+"."+callee.Name()+" in "+f.String())
					}
				}
				walk(callee)
			}
		}
	}
	walk(runFn)
	for k := range set {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds, evidence, checkpoints
}

// oracleJobWrites loads the jobs package with its dependencies' syntax and
// returns, per job ID, the write set derived from its code.
// oracleJob is what the oracle derives from one job's code.
type oracleJob struct {
	writes      []string
	evidence    map[string]string
	checkpoints bool
}

func loadOracleJobs() (map[string]oracleJob, error) {
	cfg := &packages.Config{Mode: packages.LoadAllSyntax}
	pkgs, err := packages.Load(cfg, "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs")
	if err != nil {
		return nil, fmt.Errorf("load jobs package: %w", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("the jobs package does not type-check")
	}
	prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	for _, p := range ssapkgs {
		if p != nil && strings.HasPrefix(p.Pkg.Path(), oracleModule) {
			p.Build()
		}
	}
	jp := ssapkgs[0]
	// Packages of the module that jobs reaches are built too, but only
	// ssautil's roots were; build every module package SSA created.
	for _, p := range prog.AllPackages() {
		if strings.HasPrefix(p.Pkg.Path(), oracleModule) {
			p.Build()
		}
	}
	out := map[string]oracleJob{}
	for _, m := range jp.Members {
		tn, ok := m.(*ssa.Type)
		if !ok {
			continue
		}
		ms := prog.MethodSets.MethodSet(types.NewPointer(tn.Type()))
		idSel, runSel := ms.Lookup(nil, "ID"), ms.Lookup(nil, "Run")
		if idSel == nil || runSel == nil {
			continue
		}
		id := constantReturn(prog.MethodValue(idSel))
		if id == "" {
			return nil, fmt.Errorf("job type %s: ID() does not return a constant string", tn.Name())
		}
		w, why, cp := derivedWrites(prog, prog.MethodValue(runSel))
		out[id] = oracleJob{writes: w, evidence: why, checkpoints: cp}
	}
	return out, nil
}

var (
	oracleOnce   sync.Once
	oracleResult map[string]oracleJob
	oracleErr    error
)

// oracleJobs loads and analyses the jobs package once for every test that asks.
func oracleJobs(t *testing.T) map[string]oracleJob {
	t.Helper()
	oracleOnce.Do(func() { oracleResult, oracleErr = loadOracleJobs() })
	if oracleErr != nil {
		t.Fatal(oracleErr)
	}
	return oracleResult
}

// constantReturn is the string a one-statement method returns.
func constantReturn(f *ssa.Function) string {
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if r, ok := in.(*ssa.Return); ok && len(r.Results) == 1 {
				if c, ok := r.Results[0].(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
					return constant.StringVal(c.Value)
				}
			}
		}
	}
	return ""
}

// TestJobWrites_MatchSSAOracle derives every job's write set from its code
// (the interface writes and file mutations its Run reaches) and compares it,
// kind for kind, with the Effects the native kind declares. The count-only
// check in TestKindShell_EffectsTable cannot see a swapped kind.
func TestJobWrites_MatchSSAOracle(t *testing.T) {
	if os.Getenv("JOBWRITES_ORACLE_DUMP") != "" {
		for id, j := range oracleJobs(t) {
			fmt.Printf("ORACLE %s: %v checkpoints=%v %v\n", id, j.writes, j.checkpoints, j.evidence)
		}
		return
	}
	derived := oracleJobs(t)
	defs := jobs.Kinds(jobs.KindDeps{}, nil)
	if len(defs) != len(derived) {
		t.Errorf("%d kinds but the oracle found %d jobs", len(defs), len(derived))
	}
	for _, d := range defs {
		id := strings.TrimPrefix(string(d.Kind()), "books.maintenance.")
		dj, ok := derived[id]
		want := dj.writes
		if !ok {
			t.Errorf("%s: the oracle found no job of that ID", id)
			continue
		}
		fx := tasks.CommonOf(d).Effects
		var got []string
		for _, r := range fx.WriteSet() {
			got = append(got, r.Kind)
		}
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: declared writes %v, derived from the code %v (evidence %v)", id, got, want, dj.evidence)
		}
	}
}

// TestKindShell_ResumeContinueMeansCheckpoint: a kind resumes (ResumeContinue)
// exactly when the code of its job restores a checkpoint, and every other job
// that HEAD restarts runs again from zero. The set of checkpointing jobs is
// read from the code, not from checkpointingJobs.
func TestKindShell_ResumeContinueMeansCheckpoint(t *testing.T) {
	derived := oracleJobs(t)
	for _, d := range jobs.Kinds(jobs.KindDeps{}, nil) {
		id := strings.TrimPrefix(string(d.Kind()), "books.maintenance.")
		got := tasks.CommonOf(d).Resume
		if got == tasks.ResumeContinue && !derived[id].checkpoints {
			t.Errorf("%s resumes with ResumeContinue but its Run reaches no checkpoint", id)
		}
		if derived[id].checkpoints && got != tasks.ResumeContinue {
			t.Errorf("%s checkpoints but its resume policy is %v, not continue", id, got)
		}
	}
	var cp []string
	for id, j := range derived {
		if j.checkpoints {
			cp = append(cp, id)
		}
	}
	sort.Strings(cp)
	if want := []string{"backfill-file-hashes", "recompute-book-aggregates"}; !slices.Equal(cp, want) {
		t.Errorf("jobs that checkpoint = %v, want %v", cp, want)
	}
}
