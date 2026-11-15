// file: internal/plugins/maintenance/kinds_t6_g1_test.go
// version: 1.0.5
// guid: 8b0c5c2e-6a54-4c27-9d0e-3f1a7d52e0b4
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// t6g1Recorder is the writer a test hands the kinds: it records the calls that
// reach it and the tasks.Writer NewWriters was given.
type t6g1Recorder struct {
	t6g1MergeWriter // nil: a call the test did not expect panics

	mu      sync.Mutex
	renames []string
	res     t6g1ResourceLog
}

func (r *t6g1Recorder) RenameAuthorJournaled(opID string, a database.Author, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renames = append(r.renames, opID+":"+a.Name+"->"+name)
	r.res.record("RenameAuthorJournaled")
	return nil
}

func (r *t6g1Recorder) MoveCredits(string, authormerge.CreditMove) (authormerge.MoveResult, error) {
	panic("MoveCredits is not expected in this test")
}

// t6g1JokerStore holds one stranded author, "& Joker", and no author "Joker".
type t6g1JokerStore struct{ authorConjunctionRepairStore }

func (t6g1JokerStore) GetAllAuthors() ([]database.Author, error) {
	return []database.Author{{ID: 7, Name: "& Joker"}, {ID: 8, Name: "Batman"}}, nil
}
func (t6g1JokerStore) GetAuthorByName(string) (*database.Author, error) { return nil, nil }

// t6g1Probe records how the native shells called deps.NewWriters.
type t6g1Probe struct {
	mu       sync.Mutex
	calls    int
	given    tasks.Writer
	recorder *t6g1Recorder
	caches   int
}

func (p *t6g1Probe) deps() t6g1Deps {
	return t6g1Deps{
		Stores: t6g1Stores{ConjunctionRepair: t6g1JokerStore{}},
		NewWriters: func(w tasks.Writer) t6g1Writers {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.calls++
			p.given = w
			return t6g1Writers{Merge: p.recorder}
		},
		AIJournalRetentionDays: func() int { return 30 },
		InvalidateAuthorsCache: func() { p.mu.Lock(); p.caches++; p.mu.Unlock() },
		InvalidateDedupCache:   func() { p.mu.Lock(); p.caches++; p.mu.Unlock() },
	}
}

// fakeT6G1Deps is a deps value that is enough to build the 14 definitions: the
// conformance checks read their declarations and never run a body.
func fakeT6G1Deps(t *testing.T) t6g1Deps {
	t.Helper()
	p := &t6g1Probe{recorder: &t6g1Recorder{}}
	return p.deps()
}

// t6g1GateWriter is a distinguishable tasks.Writer: identity is the assertion.
type t6g1GateWriter struct{ name string }

func (*t6g1GateWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t6g1GateWriter) MarkRowApplied(context.Context, string)           {}
func (*t6g1GateWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

// t6g1Host is a taskstest.FakeRun with the mode and writer a test chooses
// (FakeRun is pinned to ModeLive with no writer).
type t6g1Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer

	mu    sync.Mutex
	stood []tasks.Key
}

// t6g1TestHold is the hold t6g1Host hands back: FakeRun does not model
// StandDown, and a test asserts on the keys that were asked for.
type t6g1TestHold struct{}

func (t6g1TestHold) Release() {}

// StandDown records the key the run parked.
func (h *t6g1Host) StandDown(key tasks.Key) (tasks.Hold, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stood = append(h.stood, key)
	return t6g1TestHold{}, nil
}

// standDowns are the keys the run asked to park, in order.
func (h *t6g1Host) standDowns() []tasks.Key {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.stood)
}

func (h *t6g1Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g1Host) Writer() tasks.Writer { return h.w }
func (h *t6g1Host) Log() *slog.Logger    { return slog.New(slog.DiscardHandler) }

// runT6G1Full runs d's own Run, the function the executor would call, and
// returns its result and the host it ran on. pre prepares the host (a checkpoint
// an earlier attempt left, say) before the run starts.
func runT6G1Full(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string, pre func(*taskstest.FakeRun)) (json.RawMessage, *t6g1Host, error) {
	t.Helper()
	decode, task := t6g1Runner(t, d)
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := decode(raw)
	require.NoError(t, err)
	h := &t6g1Host{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode, w: w}
	if pre != nil {
		pre(h.FakeRun)
	}
	res, err := task.Run(tasks.NewRC(h), p)
	return res, h, err
}

// t6g1Runner is d's params decoder and Task body.
func t6g1Runner(t *testing.T, d tasks.Definition) (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage]) {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task, "%s is not a Task", d.Kind())
	return decode, task
}

// runT6G1Kind runs d and returns only its error.
func runT6G1Kind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) error {
	t.Helper()
	_, _, err := runT6G1Full(t, d, mode, w, params, nil)
	return err
}

func t6g1ByKind(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

func TestT6G1Kinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).t6g1Kinds(fakeT6G1Deps(t), nil)...)
}

// t6g1WriteSet is a definition's write set as sorted "module/kind" strings.
func t6g1WriteSet(c tasks.Common) []string {
	var out []string
	for _, r := range c.Effects.WriteSet() {
		out = append(out, tasks.FormatRef(r))
	}
	slices.Sort(out)
	return out
}

// t6g1WriterResources maps every method of every group 1 writer interface to
// the resources that write touches. It is keyed on the interfaces' methods, not
// on the Effects under test: TestT6G1Kinds_WriterResourceMapCoversEveryWriterMethod
// reflects over t6g1Writers and fails for a method with no row, and the spies
// record the resources of each write a live run actually makes. A live run then
// asserts the recorded resources are within the kind's declared WriteSet, so a
// wrong or missing resource in an Effects declaration fails a test instead of
// being copied into a second hand-typed table.
//
// DeleteAuthor and MergeDeleteAuthor include books: both run the store's
// deleteAuthor, whose repointPrimaryScalarsBeforeDelete calls ModifyBook on
// every book whose primary author is the one deleted (author_primary_repoint.go).
var t6g1WriterResources = map[string][]string{
	"AIJournalPruner":              {string(ops.ResAIJournal)},
	"BackfillFilterIndexWindow":    {string(ops.ResActivity)},
	"MarkFilterIndexBackfillDone":  {string(ops.ResActivity)},
	"ClearFilterIndexBackfillDone": {string(ops.ResActivity)},
	"ReconcileFilterIndexCoverage": {string(ops.ResActivity)},
	"CreateAuthor":                 {string(ops.ResAuthors)},
	"DeleteAuthor":                 {string(ops.ResBooks), string(ops.ResAuthors)},
	"ModifyBookAuthors":            {string(ops.ResAuthors)},
	"SetBookAuthors":               {string(ops.ResAuthors)},
	"ModifyBook":                   {string(ops.ResBooks)},
	"RecordOperationChange":        {string(ops.ResOperations)},
	"JournalStep":                  {string(ops.ResOperations)},
	"MoveCredits":                  {string(ops.ResBooks), string(ops.ResAuthors), string(ops.ResOperations)},
	"MergeDeleteAuthor":            {string(ops.ResBooks), string(ops.ResAuthors), string(ops.ResOperations)},
	"RenameAuthorJournaled":        {string(ops.ResAuthors), string(ops.ResOperations)},
	"SetRaw":                       {string(ops.ResAuthorityLists)},
	"DeleteRawBatch":               {string(ops.ResAuthorityLists)},
}

// t6g1ResourceLog records the resources of the writes a spy saw, and the
// methods it saw that t6g1WriterResources does not map.
type t6g1ResourceLog struct {
	mu       sync.Mutex
	seen     map[string]bool
	unmapped map[string]bool
}

func (l *t6g1ResourceLog) record(method string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	res, ok := t6g1WriterResources[method]
	if !ok {
		if l.unmapped == nil {
			l.unmapped = map[string]bool{}
		}
		l.unmapped[method] = true
		return
	}
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	for _, r := range res {
		l.seen[r] = true
	}
}

// t6g1RequireWritesWithin fails when a spy saw a write whose resource the
// definition's WriteSet does not declare, or an unmapped writer method.
func t6g1RequireWritesWithin(t *testing.T, def tasks.Definition, logs ...*t6g1ResourceLog) map[string]bool {
	t.Helper()
	declared := t6g1WriteSet(tasks.CommonOf(def))
	seen := map[string]bool{}
	for _, l := range logs {
		if l == nil {
			continue
		}
		l.mu.Lock()
		for r := range l.seen {
			seen[r] = true
		}
		require.Empty(t, l.unmapped, "%s: writer methods with no row in t6g1WriterResources", def.Kind())
		l.mu.Unlock()
	}
	for r := range seen {
		require.Contains(t, declared, "books/"+r, "%s writes %s but does not declare it in its WriteSet %v", def.Kind(), r, declared)
	}
	return seen
}

// t6g1RequireWritesDeclared is t6g1RequireWritesWithin plus: the live run made
// at least one recorded write (a case that exercised nothing proves nothing).
func t6g1RequireWritesDeclared(t *testing.T, def tasks.Definition, logs ...*t6g1ResourceLog) {
	t.Helper()
	seen := t6g1RequireWritesWithin(t, def, logs...)
	require.NotEmpty(t, seen, "%s: the live run made no recorded write", def.Kind())
}

func TestT6G1Kinds_WriterResourceMapCoversEveryWriterMethod(t *testing.T) {
	wt := reflect.TypeOf(t6g1Writers{})
	used := map[string]bool{}
	for i := range wt.NumField() {
		it := wt.Field(i).Type
		require.Equal(t, reflect.Interface, it.Kind(), "%s is not a writer interface", wt.Field(i).Name)
		for j := range it.NumMethod() {
			name := it.Method(j).Name
			used[name] = true
			require.Contains(t, t6g1WriterResources, name, "writer method %s.%s has no row in t6g1WriterResources", wt.Field(i).Name, name)
		}
	}
	for name := range t6g1WriterResources {
		require.True(t, used[name], "t6g1WriterResources row %s names no writer method", name)
	}
}

// t6g1OpsVocabulary is every Resource constant pkg/ops/effects.go declares,
// read from the source with go/ast, so adding a constant needs no edit here and
// a name that is only a string literal in a kind is not in the set. The count pin
// is deliberate: it catches a constant written without the Resource type, which
// the enumeration skips, so adding a constant means bumping the count here.
func t6g1OpsVocabulary(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("..", "..", "..", "pkg", "ops", "effects.go"), nil, 0)
	require.NoError(t, err)
	valid := map[string]bool{}
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Resource" {
				continue
			}
			for _, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					name, err := strconv.Unquote(lit.Value)
					require.NoError(t, err)
					valid[name] = true
				}
			}
		}
	}
	require.Contains(t, valid, string(ops.ResBooks), "the enumeration found no Resource constants")
	require.Len(t, valid, 23, "pkg/ops declares a different number of Resource constants; update the count with the new constant")
	return valid
}

// Every name in every kind's declared Effects (write, read and delete sets) and
// in t6g1WriterResources is a pkg/ops Resource constant. A literal that merely
// compiles, such as "aux_stores", is caught by value here.
func TestT6G1Kinds_EffectsUseOnlyOpsResources(t *testing.T) {
	valid := t6g1OpsVocabulary(t)
	for _, d := range (&Plugin{}).t6g1Kinds(fakeT6G1Deps(t), nil) {
		c := tasks.CommonOf(d)
		for _, r := range slices.Concat(c.Effects.WriteSet(), c.Effects.ReadSet(), c.Effects.DeleteSet()) {
			require.Equal(t, "books", r.Module, "%s", d.Kind())
			require.True(t, valid[r.Kind], "%s declares %q, which is not a pkg/ops resource constant", d.Kind(), r.Kind)
		}
	}
	for method, res := range t6g1WriterResources {
		for _, r := range res {
			require.True(t, valid[r], "t6g1WriterResources[%s] names %q, which is not a pkg/ops resource constant", method, r)
		}
	}
	for kind, o := range t6g1OutsideWriterSeam {
		for _, r := range o.res {
			require.True(t, valid[r], "t6g1OutsideWriterSeam[%s] names %q, which is not a pkg/ops resource constant", kind, r)
		}
	}
}

// t6g1KindByIdent maps the kind constant a shell passes to t6g1WriterOf, by its
// source name, to the kind. An ident missing here fails the test below.
var t6g1KindByIdent = map[string]tasks.Kind{
	"pruneAIJournalKind":              pruneAIJournalKind,
	"activityFilterIndexBackfillKind": activityFilterIndexBackfillKind,
	"authorSplitScanKind":             authorSplitScanKind,
	"authorConjunctionRepairKind":     authorConjunctionRepairKind,
	"authorDuplicateMergeKind":        authorDuplicateMergeKind,
	"authorIDRepairKind":              authorIDRepairKind,
	"authorPathLinkKind":              authorPathLinkKind,
	"purgeEmptyAuthorsKind":           purgeEmptyAuthorsKind,
	"authorStripMergeKind":            authorStripMergeKind,
	"authorityBuildKind":              authorityBuildKind,
}

// t6g1KindWriterFields names, for each kind that writes through a writer group,
// the t6g1Writers field its shell takes. It is read from the shells themselves
// (each t6g1WriterOf call's kind argument and the field its pick closure
// returns), so it cannot drift from what the shell does.
func t6g1KindWriterFields(t *testing.T) map[tasks.Kind]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	out := map[tasks.Kind]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		file, err := parser.ParseFile(fset, f, src, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 4 {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "t6g1WriterOf" {
				return true
			}
			pos := fset.Position(call.Pos())
			kindID, ok := call.Args[2].(*ast.Ident)
			require.True(t, ok, "%s: t6g1WriterOf's kind argument is not a constant name", pos)
			kind, known := t6g1KindByIdent[kindID.Name]
			require.True(t, known, "%s: kind %s is not in t6g1KindByIdent", pos, kindID.Name)
			lit, ok := call.Args[3].(*ast.FuncLit)
			require.True(t, ok, "%s: t6g1WriterOf's pick is not a func literal", pos)
			require.Len(t, lit.Body.List, 1, "%s: the pick closure must be a single return", pos)
			ret, ok := lit.Body.List[0].(*ast.ReturnStmt)
			require.True(t, ok && len(ret.Results) == 1, "%s: the pick closure must return one value", pos)
			sel, ok := ret.Results[0].(*ast.SelectorExpr)
			require.True(t, ok, "%s: the pick closure must return ws.<Field>", pos)
			require.NotContains(t, out, kind, "%s: %s is built twice", pos, kind)
			out[kind] = sel.Sel.Name
			return true
		})
	}
	require.Len(t, out, len(t6g1KindByIdent), "a kind in t6g1KindByIdent has no t6g1WriterOf call, or the reverse")
	return out
}

// t6g1OutsideWriterSeam lists, per kind, the declared write resources that are
// written outside the writer interface, with the reason (only the two
// fetch-driven kinds: every other write goes through a method in
// t6g1WriterResources). A kind absent from
// t6g1KindWriterField writes only that way.
var t6g1OutsideWriterSeam = map[tasks.Kind]struct {
	res []string
	why string
}{
	resolveProductionAuthorsKind: {
		res: []string{string(ops.ResBooks), string(ops.ResAuthors), string(ops.ResSeries), string(ops.ResBookFiles),
			string(ops.ResOperations), string(ops.ResMetadataCache), string(ops.ResFieldStates)},
		why: "every write is metafetch's apply behind the fetcher, built over the run's writer; no writer group is involved",
	},
	autoMatchTranscribedKind: {
		res: []string{string(ops.ResBooks), string(ops.ResAuthors), string(ops.ResBookFiles),
			string(ops.ResOperations), string(ops.ResMetadataCache), string(ops.ResFieldStates)},
		why: "every write is metafetch's apply behind the runners, built over the run's writer; no writer group is involved",
	},
}

// The read side of every kind is write-free. The write-set derivation reflects
// only over t6g1Writers; a write added to a store or reader interface would
// bypass the gated writer and every check above. Each method on each read
// dependency must start with a read verb (or be one of the named reads), and
// none may be a method t6g1WriterResources maps.
func TestT6G1Kinds_ReadDependenciesHaveNoWriteMethods(t *testing.T) {
	readVerbs := []string{
		"Get", "List", "Count", "Scan", "Can", "Verify",
		"AuthorFileRefCounts", "AuthorRefBucketCounts", "AuthorRefCounts",
		"FilterIndexBackfillDone", "FilterIndexEarliestNanos", "ReconcileSupport",
	}
	var types []reflect.Type
	st := reflect.TypeOf(t6g1Stores{})
	for i := range st.NumField() {
		types = append(types, st.Field(i).Type)
	}
	dt := reflect.TypeOf(t6g1Deps{})
	for _, name := range []string{"ActivityFilter", "PlanReader"} {
		f, ok := dt.FieldByName(name)
		require.True(t, ok, "t6g1Deps.%s", name)
		types = append(types, f.Type)
	}
	mt := reflect.TypeOf(applygate.ManualOnlyReaders{})
	for i := range mt.NumField() {
		types = append(types, mt.Field(i).Type)
	}
	for _, it := range types {
		require.Equal(t, reflect.Interface, it.Kind(), "%s is not a read interface", it)
		for j := range it.NumMethod() {
			name := it.Method(j).Name
			_, isWrite := t6g1WriterResources[name]
			require.False(t, isWrite, "%s.%s is a writer method on a read dependency: it would bypass the gated writer", it, name)
			ok := slices.ContainsFunc(readVerbs, func(v string) bool { return strings.HasPrefix(name, v) })
			require.True(t, ok, "%s.%s does not look like a read; writes belong on t6g1Writers, or add the verb to readVerbs after review", it, name)
		}
	}
}

// A kind's declared WriteSet is exactly the union of the resources of every
// method on its writer interface, plus the listed resources written outside it.
// Unlike the live subset check this does not depend on which path a fixture
// takes: dropping or adding a resource in an Effects declaration fails here
// whether or not any test reaches the write.
func TestT6G1Kinds_WriteSetEqualsTheWriterInterfaceUnion(t *testing.T) {
	wt := reflect.TypeOf(t6g1Writers{})
	kindWriterField := t6g1KindWriterFields(t)
	for _, d := range (&Plugin{}).t6g1Kinds(fakeT6G1Deps(t), nil) {
		kind := d.Kind()
		declared := map[string]bool{}
		for _, r := range tasks.CommonOf(d).Effects.WriteSet() {
			declared[r.Kind] = true
		}
		if len(declared) == 0 {
			require.NotContains(t, kindWriterField, kind, "%s is read-only", kind)
			require.NotContains(t, t6g1OutsideWriterSeam, kind, "%s is read-only", kind)
			continue
		}
		union := map[string]bool{}
		if field, ok := kindWriterField[kind]; ok {
			sf, found := wt.FieldByName(field)
			require.True(t, found, "%s: no t6g1Writers field %s", kind, field)
			for j := range sf.Type.NumMethod() {
				name := sf.Type.Method(j).Name
				res, mapped := t6g1WriterResources[name]
				require.True(t, mapped, "%s: writer method %s has no row in t6g1WriterResources", kind, name)
				for _, r := range res {
					union[r] = true
				}
			}
		}
		outside := map[string]bool{}
		if o, ok := t6g1OutsideWriterSeam[kind]; ok {
			require.NotEmpty(t, o.why, "%s: an outside-the-seam entry needs a reason", kind)
			for _, r := range o.res {
				require.False(t, union[r], "%s: %s is listed as outside the writer seam but a writer method already covers it", kind, r)
				outside[r] = true
			}
		}
		_, hasField := kindWriterField[kind]
		require.True(t, hasField || len(outside) > 0, "%s writes but has neither a writer field nor an outside-the-seam entry", kind)
		want := map[string]bool{}
		for r := range union {
			want[r] = true
		}
		for r := range outside {
			want[r] = true
		}
		require.Equal(t, t6g1SortedKeys(want), t6g1SortedKeys(declared), "%s: declared WriteSet differs from writer-interface union + outside-the-seam", kind)
	}
}

func t6g1SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Every Effects resource name in this package is a pkg/ops constant, written
// string(ops.ResX): a string literal for the name would be invisible to the
// vocabulary check's rename safety and to a search for who declares a resource.
func TestT6G1Kinds_EffectsNamesAreConstantsNotLiterals(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		file, err := parser.ParseFile(fset, f, src, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Res" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "tasks" {
				return true
			}
			checked++
			_, lit := call.Args[1].(*ast.BasicLit)
			require.False(t, lit, "%s: tasks.Res names its resource with a string literal; use string(ops.ResX)", fset.Position(call.Pos()))
			return true
		})
	}
	require.Positive(t, checked, "the scan found no tasks.Res call; it is looking in the wrong place")
}

// The native pool's watermark is the contiguous finished prefix, whatever the
// completion order: a resume from a watermark that ran ahead of an unfinished
// item would skip it, and the activity backfill would then mark the index done
// over unindexed windows.
func TestT6G1Kinds_WatermarkIsTheContiguousPrefix(t *testing.T) {
	ctx := context.Background()
	var got []int
	w := newT6g1Watermark(0, 5, 1, func(_ context.Context, wm int) error { got = append(got, wm); return nil })
	for _, i := range []int{0, 2, 3, 1} {
		require.NoError(t, w.complete(ctx, i))
	}
	require.Equal(t, []int{1, 1, 1, 4}, got, "the watermark never passes an unfinished item")

	got = nil
	w = newT6g1Watermark(3, 6, 1, func(_ context.Context, wm int) error { got = append(got, wm); return nil })
	for _, i := range []int{4, 3, 5} {
		require.NoError(t, w.complete(ctx, i))
	}
	require.Equal(t, []int{3, 5, 6}, got, "a resumed pool starts its watermark at ResumeFrom")

	got = nil
	w = newT6g1Watermark(0, 6, 3, func(_ context.Context, wm int) error { got = append(got, wm); return nil })
	for i := range 6 {
		require.NoError(t, w.complete(ctx, i))
	}
	require.Equal(t, []int{3, 6}, got, "the callback runs once per CheckpointEvery completions")
}

func TestT6G1Kinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.t6g1Kinds(fakeT6G1Deps(t), nil)
	require.Len(t, defs, 14)

	type row struct {
		head sdk.OperationDef
		// par is the parallelism the kind declares for its pool, wantPar the
		// prefix of its String(): cpu where HEAD sized the pool to NumCPU, fixed
		// where HEAD set a count. A kind with no pool declares neither.
		par     tasks.Parallelism
		wantPar string
		// checkpoints: HEAD's ResumeRestart is ResumeContinue only for a body
		// that checkpoints.
		checkpoints bool
		// readOnly: the two reports write nothing.
		readOnly bool
	}
	rows := []row{
		{head: p.activityFilterIndexBackfillDef(), par: activityFilterIndexBackfillParallelism(0), wantPar: "cpu", checkpoints: true},
		{head: p.pruneAIJournalDef()},
		{head: p.authorSplitScanDef(), par: authorSplitScanParallelism(), wantPar: "fixed(4): "},
		{head: p.resolveProductionAuthorsDef(), par: resolveProductionAuthorsParallelism(), wantPar: "fixed("},
		{head: p.authorConjunctionRepairDef()},
		{head: p.authorDuplicateMergeDef()},
		{head: p.authorIDRepairDef(), par: authorIDRepairParallelism(), wantPar: "cpu"},
		{head: p.authorPathLinkDef(), par: authorPathLinkParallelism(), wantPar: "cpu"},
		{head: p.purgeEmptyAuthorsDef()},
		{head: p.authorStripMergeDef()},
		{head: p.authorTitleFragmentScanDef(), readOnly: true},
		{head: p.authorWhitespaceCollisionReportDef(), readOnly: true},
		{head: p.authorityBuildDef(), par: authorityBuildParallelism(0), wantPar: "cpu"},
		{head: p.autoMatchTranscribedDef(), par: autoMatchTranscribedParallelism(), wantPar: "fixed(1): ", checkpoints: true},
	}

	seen := map[tasks.Kind]bool{}
	for _, r := range rows {
		id := r.head.ID
		kind := tasks.Kind("books." + id)
		seen[kind] = true
		def := t6g1ByKind(t, defs, kind)
		c := tasks.CommonOf(def)

		require.Equal(t, r.head.DisplayName, c.Title, "%s title", id)
		require.Equal(t, r.head.Description, c.Help, "%s help", id)
		wantPrio := tasks.PriorityNormal
		switch r.head.DefaultPriority {
		case sdk.PriorityLow:
			wantPrio = tasks.PriorityLow
		case sdk.PriorityHigh:
			wantPrio = tasks.PriorityHigh
		}
		require.Equal(t, wantPrio, c.Priority, "%s priority", id)
		require.Equal(t, r.head.Timeout, c.Timeout, "%s timeout", id)
		require.Equal(t, tasks.Key("books."+r.head.ConcurrencyKey), c.ConcurrencyKey, "%s concurrency key", id)
		if r.head.ProgressTimeout > 0 {
			require.Equal(t, r.head.ProgressTimeout, c.ProgressTimeout, "%s progress timeout", id)
		}

		var wantResume tasks.ResumePolicy
		switch r.head.ResumePolicy {
		case sdk.ResumeRestart:
			wantResume = tasks.ResumeFromZero
			if r.checkpoints {
				wantResume = tasks.ResumeContinue
			}
		case sdk.ResumeRequeue:
			wantResume = tasks.ResumeFromZero
		case sdk.ResumeDrop:
			wantResume = tasks.ResumeDrop
		case sdk.ResumeAsk:
			wantResume = tasks.ResumeAsk
		default:
			t.Fatalf("%s: HEAD resume policy %v has no mapping", id, r.head.ResumePolicy)
		}
		require.Equal(t, wantResume, c.Resume, "%s resume", id)

		if r.readOnly {
			require.True(t, c.Effects.IsReadOnly(), "%s must be declared ReadOnly", id)
		} else {
			require.False(t, c.Effects.IsReadOnly(), "%s writes", id)
			require.NotEmpty(t, c.Effects.WriteSet(), "%s declares no write", id)
		}

		if r.wantPar != "" {
			require.True(t, strings.HasPrefix(r.par.String(), r.wantPar), "%s parallelism %q, want prefix %q", id, r.par.String(), r.wantPar)
		}
		require.Contains(t, c.Ready, t6g1SignalMemDB, "%s readiness", id)
	}
	require.Len(t, seen, 14, "14 distinct kinds")

	// The declared parallelism follows the param that sets it, and is never a
	// resolved NumCPU wrapped in Fixed nor a clamp of a non-positive count.
	require.Equal(t, "cpu", activityFilterIndexBackfillParallelism(0).String())
	require.Equal(t, "cpu", activityFilterIndexBackfillParallelism(-3).String())
	require.True(t, strings.HasPrefix(activityFilterIndexBackfillParallelism(3).String(), "fixed(3): "))
	require.Equal(t, "cpu", authorityBuildParallelism(0).String())
	require.True(t, strings.HasPrefix(authorityBuildParallelism(2).String(), "fixed(2): "))
	require.True(t, strings.HasPrefix(authorityBuildParallelism(authorityMaxConcurrency+50).String(),
		fmt.Sprintf("fixed(%d): ", authorityMaxConcurrency)), "the caller's count is capped at authorityMaxConcurrency")
}

func TestT6G1Kinds_WritesThroughShell(t *testing.T) {
	probe := &t6g1Probe{recorder: &t6g1Recorder{}}
	defs := (&Plugin{}).t6g1Kinds(probe.deps(), nil)
	conj := t6g1ByKind(t, defs, authorConjunctionRepairKind)
	gate := &t6g1GateWriter{name: "gotham"}

	// Preview reports and writes nothing.
	require.NoError(t, runT6G1Kind(t, conj, tasks.ModePreview, gate, ""))
	require.Empty(t, probe.recorder.renames, "a preview wrote")
	require.Zero(t, probe.caches, "a preview dropped the caches")

	// Live renames through the writer NewWriters built over the run's writer.
	probe.calls = 0
	require.NoError(t, runT6G1Kind(t, conj, tasks.ModeLive, gate, ""))
	require.Equal(t, 1, probe.calls)
	require.Same(t, gate, probe.given, "NewWriters must be given the run's writer")
	require.Len(t, probe.recorder.renames, 1)
	require.Contains(t, probe.recorder.renames[0], "& Joker->Joker")
	require.Positive(t, probe.caches, "a live write drops the caches")
	t6g1RequireWritesDeclared(t, conj, &probe.recorder.res)

	// A live-only kind refuses Preview before it builds a writer.
	probe.calls = 0
	for _, kind := range []tasks.Kind{pruneAIJournalKind, activityFilterIndexBackfillKind} {
		err := runT6G1Kind(t, t6g1ByKind(t, defs, kind), tasks.ModePreview, gate, "")
		require.Error(t, err, "%s", kind)
		require.Contains(t, err.Error(), "preview not supported", "%s", kind)
	}
	require.Zero(t, probe.calls, "a refused preview built a writer")
}
