// file: internal/maintenance/jobs/testdata/oraclefix/fix/fix.go
// version: 1.8.0
// guid: 164bc002-83bc-4523-bed7-b8bfebfc2c9a
// last-edited: 2026-11-19

// Package fix holds the shapes of code the write-set oracle has to follow. Each
// exported function is a root the oracle's tests start a walk from; the store
// calls under it are the writes the walk must find (or, for the Read and
// Shared roots, must not).
package fix

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs/testdata/oraclefix/internal/books/store"
)

// undoer is an interface whose method is named like no write at all, and whose
// one implementation writes through the store.
type undoer interface{ Undo(id string) error }

type storeUndoer struct{ s *store.Store }

func (u storeUndoer) Undo(id string) error { return u.s.ModifyBook(id) }

// ViaInterface writes a book through an interface call into a wrapper.
func ViaInterface(s *store.Store, id string) error {
	var u undoer = storeUndoer{s}
	return u.Undo(id)
}

// step is a store operation passed around as a value.
type step func(s *store.Store, id string) error

var steps []step

func init() { steps = append(steps, dropAuthor) }

// dropAuthor is registered in init, under another name, and reaches the store
// only when a caller invokes a step.
func dropAuthor(s *store.Store, id string) error { return s.DeleteAuthor(id) }

// ViaFuncValue writes an author through a call to a function value read from a
// package variable.
func ViaFuncValue(s *store.Store, id string) error {
	for _, st := range steps {
		if err := st(s, id); err != nil {
			return err
		}
	}
	return nil
}

type worker struct{ apply step }

func newWorker() *worker { return &worker{apply: renamedModify} }

func renamedModify(s *store.Store, id string) error { return s.ModifyBook(id) }

var theWorker = newWorker()

// ViaStructField writes a book through a function value stored in a struct field
// by a constructor this function never calls.
func ViaStructField(s *store.Store, id string) error { return theWorker.apply(s, id) }

// ViaNonVerbStoreMethod writes a book through a store method whose name starts
// with no write verb.
func ViaNonVerbStoreMethod(s *store.Store, id string) error {
	return s.RevertBookToVersion(id)
}

// runEach is a helper two roots share; its callback is whatever the caller
// passes.
func runEach(s *store.Store, ids []string, fn step) error {
	for _, id := range ids {
		if err := fn(s, id); err != nil {
			return err
		}
	}
	return nil
}

func readOnly(s *store.Store, id string) error { return s.GetBookByID(id) }

// SharedReadOnly passes a reading callback to the shared helper. It writes
// nothing; reporting a write here means the walk treated the helper's callback as
// every callback any caller passes (SharedWriter's dropAuthor).
func SharedReadOnly(s *store.Store, ids []string) error { return runEach(s, ids, readOnly) }

// SharedWriter passes a writing callback to the shared helper.
func SharedWriter(s *store.Store, ids []string) error { return runEach(s, ids, dropAuthor) }

// Read only reads.
func Read(s *store.Store, id string) error { return s.GetBookByID(id) }

// runEachOrDefault is a helper whose callback is the caller's, or, when the
// caller passes none, one read from a package variable. The value mixes a
// parameter with a field load, so the call graph (which cannot tell the two
// apart, and is context-insensitive) is not asked about it.
func runEachOrDefault(s *store.Store, ids []string, fn step) error {
	if fn == nil {
		fn = steps[0]
	}
	for _, id := range ids {
		if err := fn(s, id); err != nil {
			return err
		}
	}
	return nil
}

// MixedReadOnly passes a reading callback to a helper that falls back to a
// package variable's step. It writes nothing: the callback is this caller's.
func MixedReadOnly(s *store.Store, ids []string) error { return runEachOrDefault(s, ids, readOnly) }

// orphanWriter is a module interface nothing implements: no call graph has a
// callee for it, so the call is judged by the interface method's own name.
type orphanWriter interface{ SaveBook(id string) error }

// ViaOrphanInterface writes a book through an interface with no implementation.
func ViaOrphanInterface(w orphanWriter, id string) error { return w.SaveBook(id) }

// Each is a generic function of this module; its instance and the closure passed
// to it have no package of their own, and belong to this one.
func Each[T any](xs []T, f func(T) error) error {
	for _, x := range xs {
		if err := f(x); err != nil {
			return err
		}
	}
	return nil
}

// ViaGeneric writes a book from a closure handed to an instance of a generic of
// this module.
func ViaGeneric(s *store.Store, ids []string) error {
	return Each(ids, func(id string) error { return s.ModifyBook(id) })
}

// UsesStdGeneric calls an instance of a standard library generic, which is not
// module code and is not followed.
func UsesStdGeneric(ids []string) []string {
	slices.Sort(ids)
	return ids
}

// progressReporter is progress reporting: a call to one is not judged by its
// method name (the oracle's oracleNotAWrite names the Reporter types), but what
// implements it is still walked.
type progressReporter interface{ Report(id string) error }

type quietReporter struct{}

func (quietReporter) Report(string) error { return nil }

type storeReporter struct{ s *store.Store }

func (r storeReporter) Report(id string) error { return r.s.ModifyBook(id) }

// ReportsProgress calls a progress reporter whose implementation happens to
// write through the store: the call's name is not a write, the implementation's
// body is.
func ReportsProgress(s *store.Store, id string) error {
	var r progressReporter = storeReporter{s}
	return r.Report(id)
}

// ReportsQuietly calls a progress reporter that does nothing: no write.
func ReportsQuietly(id string) error {
	var r progressReporter = quietReporter{}
	return r.Report(id)
}

// runEachConverted is runEach with the callback converted before it is called.
func runEachConverted(s *store.Store, ids []string, fn func(*store.Store, string) error) error {
	for _, id := range ids {
		if err := step(fn)(s, id); err != nil {
			return err
		}
	}
	return nil
}

// ConvertedReadOnly passes a reading callback to a helper that converts it: the
// callback is still this caller's.
func ConvertedReadOnly(s *store.Store, ids []string) error {
	return runEachConverted(s, ids, readOnly)
}

// ConvertedWriter is ConvertedReadOnly's sensitivity check: the same helper with
// a writing callback.
func ConvertedWriter(s *store.Store, ids []string) error {
	return runEachConverted(s, ids, dropAuthor)
}

// ViaEitherField calls a function value that is one of two package-level
// values, chosen at run time: the graph resolves both.
func ViaEitherField(s *store.Store, id string, first bool) error {
	fn := steps[0]
	if !first {
		fn = theWorker.apply
	}
	return fn(s, id)
}

// bookModifier is a module interface the store's pointer type implements, beside
// an in-program type that does not write. The program passes only the second to
// ViaPartialImpls, so a call graph built from what flows in the program knows one
// implementation; the production wiring passes the other.
type bookModifier interface{ ModifyBook(id string) error }

type dryRun struct{}

func (dryRun) ModifyBook(string) error { return nil }

// ViaPartialImpls calls the interface method of a value whose set of
// implementations the program knows only in part.
func ViaPartialImpls(m bookModifier, id string) error { return m.ModifyBook(id) }

// DryRunCaller is the one in-program caller of ViaPartialImpls.
func DryRunCaller(id string) error { return ViaPartialImpls(dryRun{}, id) }

// remoteSaver is implemented by a followed type whose body leaves the module
// without a write verb in sight.
type remoteSaver interface{ SaveBook(id string) error }

type flushSaver struct{ w *bufio.Writer }

func (f flushSaver) SaveBook(string) error { return f.w.Flush() }

// ViaOutsideBody calls an interface method whose one known implementation writes
// through code outside the module: the walk cannot follow the body out, so the
// method's own name is the only evidence.
func ViaOutsideBody(r remoteSaver, id string) error { return r.SaveBook(id) }

// OutsideCaller is the one in-program caller of ViaOutsideBody.
func OutsideCaller(w *bufio.Writer, id string) error { return ViaOutsideBody(flushSaver{w}, id) }

// ViaFieldCallback passes a function value loaded from a struct field to a shared
// helper.
func ViaFieldCallback(s *store.Store, ids []string) error {
	return runEach(s, ids, theWorker.apply)
}

// ViaGlobalCallback passes a function value loaded from a package-level slice.
func ViaGlobalCallback(s *store.Store, ids []string) error {
	return runEach(s, ids, steps[0])
}

// ViaNilDefault passes nothing and lets the helper fall back to its default.
func ViaNilDefault(s *store.Store, ids []string) error {
	return runEachOrDefault(s, ids, nil)
}

type ctxKey struct{}

// ViaContextFunc calls a function value carried in a context, set by code the
// program does not contain.
func ViaContextFunc(ctx context.Context, s *store.Store, id string) error {
	fn, _ := ctx.Value(ctxKey{}).(step)
	return fn(s, id)
}

// oddFn has a signature no other function in the fixture has.
type oddFn func(float64) float32

var registry sync.Map

// ViaUnresolved calls a function value from a map filled by code outside the
// program, of a signature nothing in the program has: no graph can name a callee.
func ViaUnresolved() float32 {
	v, _ := registry.Load("k")
	f, _ := v.(oddFn)
	return f(1)
}

// ViaGenericStoreFn calls an instance of a generic function of the store package.
func ViaGenericStoreFn(s *store.Store) error { return store.UpdateWith(s, 1) }

// unsetHook is a package variable nothing in the program stores to: a hook that
// only code outside the program sets.
var unsetHook func()

// ViaUnsetHook calls a declared variable the module never stores a function into.
func ViaUnsetHook() { unsetHook() }

// fetchHook returns a function it took from a registry filled outside the program.
func fetchHook() func() {
	v, _ := registry.Load("h")
	f, _ := v.(func())
	return f
}

// ViaCallResult calls the result of a module function whose return statements
// name no function.
func ViaCallResult() { fetchHook()() }

// ViaOutsideClosure calls a closure that code outside the module made.
func ViaOutsideClosure(ctx context.Context) {
	_, cancel := context.WithCancel(ctx)
	cancel()
}

// unwiredUndoer is a module interface whose method name starts with no write verb.
type unwiredUndoer interface{ Undo(id string) error }

type unwiredStoreUndoer struct{ s *store.Store }

func (u unwiredStoreUndoer) Undo(id string) error { return u.s.ModifyBook(id) }

// ViaUnwiredInterface calls a non-verb method on a value nothing in the program
// passes (the wiring outside it does): only the signature-based class hierarchy
// knows the implementation.
func ViaUnwiredInterface(u unwiredUndoer, id string) error { return u.Undo(id) }

var stepByName = map[string]step{"drop": dropAuthor}

// ViaMapCallback passes a function value loaded from a package-level map.
func ViaMapCallback(s *store.Store, ids []string) error {
	return runEach(s, ids, stepByName["drop"])
}

var unsetStep step

// runEachUnset is a helper with one caller, which passes a variable nothing
// stores a function into.
func runEachUnset(s *store.Store, id string, fn step) error { return fn(s, id) }

// ViaUnsetStepParam hands a helper a callback read from an unset variable.
func ViaUnsetStepParam(s *store.Store, id string) error { return runEachUnset(s, id, unsetStep) }

// ViaPhiOfHooks calls a value that is one of two reads of an unset variable.
func ViaPhiOfHooks(first bool) {
	f := unsetHook
	if !first {
		f = unsetHook
	}
	f()
}

// --- Round 3: the default-deny shapes. Each root below was walked to no target
// and no UNRESOLVED key by the round-2 dispatch.

// injJob holds a callback a constructor takes as a parameter (shape a). The
// constructor is exported, so code outside the program (the wiring) can call it
// with any function.
type injJob struct{ apply step }

// NewInjJob stores its parameter into the field.
func NewInjJob(fn step) *injJob { return &injJob{apply: fn} }

var defaultInj = NewInjJob(renamedModify)

// ViaInjectedField calls a field of a parameter, filled by the constructor's
// parameter (shape a, the direct call).
func ViaInjectedField(j *injJob, s *store.Store, id string) error { return j.apply(s, id) }

// ViaInjectedFieldCallback hands the same field to a shared helper (shape a, the
// operand form: round 1 B2's RunItems(..., j.deps.Apply)).
func ViaInjectedFieldCallback(j *injJob, s *store.Store, ids []string) error {
	return runEach(s, ids, j.apply)
}

// settable is a package variable with an in-program default and a setter that
// stores its parameter (shape b): the wiring can replace the default.
var settable step = dropAuthor

// SetSettable is the setter.
func SetSettable(fn step) { settable = fn }

// ViaDefaultPlusSetter calls the variable.
func ViaDefaultPlusSetter(s *store.Store, id string) error { return settable(s, id) }

var unsetStepB step

// runEachOrUnset falls back to a variable nothing stores to (shape c).
func runEachOrUnset(s *store.Store, id string, fn step) error {
	if fn == nil {
		fn = unsetStepB
	}
	return fn(s, id)
}

// ViaParamOrUnset passes nil, so the unset default is what runs.
func ViaParamOrUnset(s *store.Store, id string) error { return runEachOrUnset(s, id, nil) }

var unsetStepC step

func forwardA(s *store.Store, id string, fn step) error { return forwardB(s, id, fn) }

func forwardB(s *store.Store, id string, fn step) error { return fn(s, id) }

// ViaTwoLevelForward hands an unset variable through two forwarding helpers
// (shape d).
func ViaTwoLevelForward(s *store.Store, id string) error { return forwardA(s, id, unsetStepC) }

func callHook(h func()) { h() }

// ViaHelperOfCallResult hands a helper the result of a call that names no
// function (shape d).
func ViaHelperOfCallResult() { callHook(fetchHook()) }

// ViaHelperOfOutsideClosure hands a helper a closure code outside the module
// made (shape d): the outside-closure allowlist applies to it.
func ViaHelperOfOutsideClosure(ctx context.Context) {
	_, cancel := context.WithCancel(ctx)
	callHook(cancel)
}

// ViaCopiedSteps copies a package-level slice into a local one and calls an
// element (shape e).
func ViaCopiedSteps(s *store.Store, id string) error {
	fns := make([]step, len(steps))
	copy(fns, steps)
	return fns[0](s, id)
}

// ViaRangeCopied fills a local slice from a parameter's elements (shape e).
func ViaRangeCopied(s *store.Store, id string, src []step) error {
	fns := make([]step, len(src))
	for i, f := range src {
		fns[i] = f
	}
	return fns[0](s, id)
}

type hooks struct{ fire func() }

// ViaHandedOutside hands a field nothing stores to to code outside the module,
// which calls it (shape f).
func ViaHandedOutside(ctx context.Context, h *hooks) { context.AfterFunc(ctx, h.fire) }

// partialUndoer has a non-verb method; the program passes only the dry run, the
// wiring passes the store-backed one (M1).
type partialUndoer interface{ Rollback(id string) error }

type dryUndo struct{}

func (dryUndo) Rollback(string) error { return nil }

type storeRollback struct{ s *store.Store }

func (r storeRollback) Rollback(id string) error { return r.s.ModifyBook(id) }

// ViaPartialNonVerb calls a non-verb interface method whose set of
// implementations the program knows only in part.
func ViaPartialNonVerb(u partialUndoer, id string) error { return u.Rollback(id) }

// DryUndoCaller is the one in-program caller of ViaPartialNonVerb.
func DryUndoCaller(id string) error { return ViaPartialNonVerb(dryUndo{}, id) }

// rewinder is a module interface with a non-verb method nothing in the program
// implements.
type rewinder interface{ Rewind(id string) error }

// ViaOrphanNonVerb calls it: no implementation, no verb.
func ViaOrphanNonVerb(r rewinder, id string) error { return r.Rewind(id) }

// ViaOutsideIface calls a non-verb method of an interface declared outside the
// module.
func ViaOutsideIface(x fmt.Stringer) string { return x.String() }

func applyTo(fn func()) { fn() }

// ViaOnlyAsValue hands out a helper as a value: no walked caller supplies the
// helper's parameter, so the call through it is the caller-outside-the-walk's.
func ViaOnlyAsValue() func(func()) { return applyTo }

// opsSink stands in for the operations registry's own store: the op-error rows
// a run's reporter persists for every run.
type opsSink interface{ InsertOpErrorV2(msg string) error }

type runReporter struct{ ops opsSink }

// Log is the reporter persisting its own log line.
func (r *runReporter) Log(msg string) { _ = r.ops.InsertOpErrorV2(msg) }

// ViaReporterOwn logs through the run's reporter.
func ViaReporterOwn(r *runReporter) { r.Log("Joker escaped Arkham") }

// ViaJobWritesOpErrors makes the same store call from job code.
func ViaJobWritesOpErrors(s opsSink) { _ = s.InsertOpErrorV2("Joker escaped Arkham") }

// jokerError is a module type implementing an interface declared outside the
// module whose method, named like no write, writes.
type jokerError struct{ s *store.Store }

func (e jokerError) Error() string { _ = e.s.ModifyBook("joker"); return "Joker" }

// ViaOpenError calls error.Error on a value an exported function's caller
// passes: the module's implementations are walked, and the others recorded.
func ViaOpenError(err error) string { return err.Error() }

// fillStep stores a function through the pointer it is handed: the caller's
// variable gets a value its own stores into it do not show.
func fillStep(p *step) { *p = func(s *store.Store, id string) error { return s.ModifyBook(id) } }

// ViaEscapedCell calls a local variable whose address it hands to a helper that
// fills it (round 3: a cell whose address is passed on is open).
func ViaEscapedCell(s *store.Store) error {
	var scarecrow step
	fillStep(&scarecrow)
	return scarecrow(s, "fear-toxin")
}

// ViaFilledByValue calls an element of a slice it built and handed to a
// function value, which can fill it (round 3: a container handed on is open).
func ViaFilledByValue(s *store.Store, fill func([]step)) error {
	gotham := make([]step, 1)
	fill(gotham)
	return gotham[0](s, "arkham")
}

// batFile is a module interface whose only implementation is outside the module
// (*os.File): the walk reads no body of its methods (round-3 review M1).
type batFile interface{ Sync() error }

// ViaOutsideOnlyImpl calls a non-verb method of a module interface implemented
// only outside the module.
func ViaOutsideOnlyImpl(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var b batFile = f
	return b.Sync()
}
