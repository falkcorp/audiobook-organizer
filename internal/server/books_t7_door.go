// file: internal/server/books_t7_door.go
// version: 1.0.1
// guid: 3a7f6c52-1d0e-4b9b-8e4c-6f21b7d0a9e5
// last-edited: 2026-11-28

package server

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/mediacore/tasks"
)

// The door of the t7 host adapter (see books_t7_adapter.go): the one exported
// way in, and the table of what is built. The call map it reads is in
// books_t7_reach.go.

// ErrKindNotBuilt is the refusal of anything the adapter does not build yet.
// A stub method returns it with Method and Kinds set (the kinds whose cores
// reach the method); the door reports it with Kind and Unbuilt set for every
// kind it leaves out. errors.As finds it through any wrapping.
type ErrKindNotBuilt struct {
	// Kind is the kind the door left out; empty on a method's refusal.
	Kind tasks.Kind
	// Unbuilt is the methods of Kind's row that are not built; set with Kind.
	Unbuilt []string
	// Method is the adapter method that refused; empty on the door's report.
	Method string
	// Kinds are the kinds that reach Method, from the call map; set with Method.
	Kinds []tasks.Kind
}

func (e *ErrKindNotBuilt) Error() string {
	if e.Method == "" {
		return fmt.Sprintf("books t7: kind %s is not built (unbuilt methods: %s)", e.Kind, strings.Join(e.Unbuilt, ", "))
	}
	kinds := make([]string, len(e.Kinds))
	for i, k := range e.Kinds {
		kinds[i] = string(k)
	}
	if len(kinds) == 0 {
		return fmt.Sprintf("books t7: %s is not built (no kind reaches it)", e.Method)
	}
	return fmt.Sprintf("books t7: %s is not built (reached by %s)", e.Method, strings.Join(kinds, ", "))
}

// booksT7NotBuilt is the refusal a stub with an error result returns.
func booksT7NotBuilt(method string) error {
	return &ErrKindNotBuilt{Method: method, Kinds: booksT7KindsReaching(method)}
}

// booksT7StubEntries counts the entries into stub bodies that have no error
// result to refuse with. The door never lets one run, so a cell asserts that
// building it moves the count by nothing.
var booksT7StubEntries atomic.Int64

// booksT7Entered is the whole body of a stub with no error result: it records
// the entry and the stub returns zero values.
func booksT7Entered(string) { booksT7StubEntries.Add(1) }

// booksT7KindsReaching is the kinds whose row lists method, sorted.
func booksT7KindsReaching(method string) []tasks.Kind {
	var out []tasks.Kind
	for kind, methods := range booksT7Reaches {
		if slices.Contains(methods, method) {
			out = append(out, kind)
		}
	}
	slices.Sort(out)
	return out
}

// booksT7Built is the methods a group has built, keyed by method name; "Ready"
// is the one name both views have, so its two views are "Store.Ready" and
// "Writer.Ready". A group sets a method here in the commit that gives it a body,
// and a cell fails when this table and the method's behaviour disagree.
var booksT7Built = map[string]bool{}

// booksT7MethodBuilt reports whether a row entry is built.
func booksT7MethodBuilt(method string) bool {
	if method == "Ready" {
		return booksT7Built["Store.Ready"] && booksT7Built["Writer.Ready"]
	}
	return booksT7Built[method]
}

// booksT7Present is the check for each *Server field a method of the adapter
// can need, keyed by field name. Every field name in booksT7Needs has one.
var booksT7Present = map[string]func(*Server) bool{
	"store":                   func(s *Server) bool { return s.store != nil },
	"scanService":             func(s *Server) bool { return s.scanService != nil },
	"organizeService":         func(s *Server) bool { return s.organizeService != nil },
	"mergeService":            func(s *Server) bool { return s.mergeService != nil },
	"dedupEngine":             func(s *Server) bool { return s.dedupEngine != nil },
	"metadataFetchService":    func(s *Server) bool { return s.metadataFetchService != nil },
	"diagnosticsService":      func(s *Server) bool { return s.diagnosticsService != nil },
	"aiScanStore":             func(s *Server) bool { return s.aiScanStore != nil },
	"pipelineManager":         func(s *Server) bool { return s.pipelineManager != nil },
	"opRegistry":              func(s *Server) bool { return s.opRegistry != nil },
	"authorsCache":            func(s *Server) bool { return s.authorsCache != nil },
	"dedupCache":              func(s *Server) bool { return s.dedupCache != nil },
	"seriesCache":             func(s *Server) bool { return s.seriesCache != nil },
	"applyWhenScannedHandler": func(s *Server) bool { return s.applyWhenScannedHandler != nil },
	"queuedOrganizeRunner":    func(s *Server) bool { return s.queuedOrganizeRunner != nil },
	"audiobookService":        func(s *Server) bool { return s.audiobookService != nil },
}

// booksT7MissingFields is the *Server fields the methods of a row need that s
// has not set, sorted and without repeats: the store, and the fields of
// booksT7Needs for each method. booksT7Needs is the fields a head method reads
// itself (two calls deep); a field a head* shell captures at construction is
// not in it, and the group that builds the method adds it.
func booksT7MissingFields(s *Server, methods []string) []string {
	var missing []string
	// Every method reads the store through storeForWiring, whatever its head*
	// shell captures at construction, so the store is needed by every kind.
	if !booksT7Present["store"](s) {
		missing = append(missing, "store")
	}
	for _, m := range methods {
		for _, field := range booksT7Needs[m] {
			// A field with no presence check is reported, never called: the
			// tables cell fails on it, and the door must not panic meanwhile.
			check := booksT7Present[field]
			if (check == nil || !check(s)) && !slices.Contains(missing, field) {
				missing = append(missing, field)
			}
		}
	}
	slices.Sort(missing)
	return missing
}

// BooksT7Result is what the door built: the definitions of the kinds whose every
// method is built, and one report for each kind it left out.
type BooksT7Result struct {
	Defs     []tasks.Definition
	NotBuilt []*ErrKindNotBuilt
}

// BooksT7Kinds is the door the books module's registration calls: the kinds of
// t7KindList, built over the adapter. It returns definitions, not the t7Deps,
// because t7Deps is unexported. sched is books' one schedule hook (R9).
//
// It builds a kind only when every method of the kind's row in booksT7Reaches
// is built, so a stub body never runs: a kind that reaches an unbuilt method is
// left out of Defs and reported in NotBuilt, which is the refusal. A built kind
// whose backing field on s is nil is an error that names the field, so a nil
// backing never reaches a method at run time; so is a kind with no row.
func (s *Server) BooksT7Kinds(standDownKey tasks.Key, sched func(tasks.Kind) *tasks.Schedule) (BooksT7Result, error) {
	var res BooksT7Result
	var problems []error
	for _, def := range t7KindList(s, s.booksT7Deps(standDownKey), sched) {
		kind := def.Kind()
		methods, ok := booksT7Reaches[kind]
		if !ok {
			problems = append(problems, fmt.Errorf("books t7: kind %s has no row in the adapter's call map (books_t7_reach.go)", kind))
			continue
		}
		var unbuilt []string
		for _, m := range methods {
			if !booksT7MethodBuilt(m) {
				unbuilt = append(unbuilt, m)
			}
		}
		if len(unbuilt) > 0 {
			res.NotBuilt = append(res.NotBuilt, &ErrKindNotBuilt{Kind: kind, Unbuilt: unbuilt})
			continue
		}
		if missing := booksT7MissingFields(s, methods); len(missing) > 0 {
			problems = append(problems, fmt.Errorf("books t7: kind %s needs Server.%s, which is not set", kind, strings.Join(missing, ", Server.")))
			continue
		}
		res.Defs = append(res.Defs, def)
	}
	if len(problems) > 0 {
		return BooksT7Result{}, errors.Join(problems...)
	}
	return res, nil
}

// BooksT7Stubs is the adapter methods that still refuse, as "Store.<name>" and
// "Writer.<name>", sorted. It calls every method of both views once with zero
// arguments and counts a method a stub when it returns an ErrKindNotBuilt or
// enters a stub body; a method that does anything else, including one that
// cannot run on this Server, is not. The tasks-flip gate in internal/books
// fails while it is not empty.
func (s *Server) BooksT7Stubs() []string {
	deps := s.booksT7Deps("")
	var out []string
	for _, v := range []struct {
		side string
		impl any
	}{
		{"Store", deps.Store},
		{"Writer", deps.NewWriter(nil)},
	} {
		rv := reflect.ValueOf(v.impl)
		for i := range rv.NumMethod() {
			name := rv.Type().Method(i).Name
			if booksT7CallIsStub(rv.Method(i)) {
				out = append(out, v.side+"."+name)
			}
		}
	}
	slices.Sort(out)
	return out
}

// booksT7CallIsStub calls fn with zero arguments and reports whether it refused
// as a stub does. A panic is not a refusal: it is a method that is built and
// cannot run on a zero Server.
func booksT7CallIsStub(fn reflect.Value) (stub bool) {
	ft := fn.Type()
	n := ft.NumIn()
	if ft.IsVariadic() {
		n--
	}
	args := make([]reflect.Value, n)
	for i := range args {
		args[i] = reflect.Zero(ft.In(i))
	}
	before := booksT7StubEntries.Load()
	defer func() {
		if recover() != nil {
			stub = false
		}
	}()
	res := fn.Call(args)
	if booksT7StubEntries.Load() != before {
		return true
	}
	if k := len(res) - 1; k >= 0 && ft.Out(k) == reflect.TypeFor[error]() {
		var nb *ErrKindNotBuilt
		err, _ := res[k].Interface().(error)
		return errors.As(err, &nb)
	}
	return false
}
