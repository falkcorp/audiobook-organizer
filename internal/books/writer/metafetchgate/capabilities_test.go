// file: internal/books/writer/metafetchgate/capabilities_test.go
// version: 1.1.0
// guid: 0f6b2d84-93c1-4e75-a8d2-5b7e1c40a9f3
// last-edited: 2026-11-19

package metafetchgate_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/metafetchgate"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// class is what the wrapper does with a store capability.
type class string

const (
	// hiddenWrite: the real store has it and the wrapper must not. The consumer
	// finds no capability and skips the write (fail closed). Resolving it
	// against the inner store (an Unwrap) would write past the gate.
	hiddenWrite class = "hidden write"
	// forwardedRead: the real store has it and the wrapper has it too, a method
	// of its own or one promoted from metafetch.Store. A read stamps nothing,
	// and hiding it changes what the consumer does without an error.
	forwardedRead class = "forwarded read"
	// implementedByNoStore: no store implements it (a test double may), so the
	// real store must not either. If one starts to, this fails until someone
	// classifies it as a write or a read.
	implementedByNoStore class = "implemented by no store"
)

// capability is one optional interface that code the fetch service hands its
// store to resolves from it.
type capability struct {
	name  string
	class class
	// has reports whether v offers the capability, resolved as the consumer
	// resolves it (database.AsCapability, so through Unwrap only).
	has func(v any) bool
	// sinks are how the consumers reach it: the type argument of an
	// AsCapability call ("AsCapability[T]"), or the name of a database helper
	// that resolves it (the scan below finds both).
	sinks []string
	// methods are the capability's methods, for the forwarded reads: the
	// wrapper's method set beyond metafetch.Store must be exactly these.
	methods []string
}

func has[T any](v any) bool { _, ok := database.AsCapability[T](v); return ok }

// seriesRefGetter is the store package's own unexported getter interface,
// matched by method set, which is all a type assertion looks at.
type seriesRefGetter interface {
	GetSeriesByID(int) (*database.Series, error)
}

// capabilities is every optional capability the code the wrapper's store reaches
// resolves. TestEveryCapabilityTheConsumersResolveIsClassified fails on a
// resolution site that is in none of the rows.
var capabilities = []capability{
	{name: "merge.UserProgressMerger", class: hiddenWrite, has: has[merge.UserProgressMerger],
		sinks: []string{"AsCapability[merge.UserProgressMerger]"}},
	{name: "SyncIdentityStore", class: hiddenWrite,
		has:   func(v any) bool { return database.AsSyncIdentityStore(v) != nil },
		sinks: []string{"AsSyncIdentityStore"}},
	{name: "backup.Checkpointable", class: hiddenWrite, has: has[backup.Checkpointable],
		sinks: []string{"AsCapability[backup.Checkpointable]"}},
	{name: "database.FileProvenanceRecorder", class: hiddenWrite, has: has[database.FileProvenanceRecorder],
		sinks: []string{"AsCapability[database.FileProvenanceRecorder]"}},

	{name: "authorcredit alias lookup", class: forwardedRead, has: has[authorcredit.AliasFinder],
		sinks: []string{"AsCapability[AliasFinder]"}, methods: []string{"FindAuthorByAlias"}},
	{name: "authorcredit.TitleSource", class: forwardedRead, has: has[authorcredit.TitleSource],
		sinks: []string{"AsCapability[TitleSource]"}, methods: []string{"GetAllBooksCore", "GetAllSeries"}},
	{name: "authorcredit.HistorySource", class: forwardedRead, has: has[authorcredit.HistorySource],
		sinks: []string{"AsCapability[HistorySource]"}, methods: []string{"GetMetadataChangeHistory"}},
	{name: "authorcredit by-author book listing", class: forwardedRead, has: has[authorcredit.AuthorBooksSource],
		sinks: []string{"AsCapability[AuthorBooksSource]"}, methods: []string{"GetBooksByAuthorIDWithRoleCore"}},
	{name: "database.RetiredBookFileReader", class: forwardedRead, has: has[database.RetiredBookFileReader],
		sinks: []string{"BookFileByPathIncludingRetired"},
		methods: []string{"GetAllBookFilesCoreIncludingRetired", "GetBookFileByPIDIncludingRetired", "GetBookFileByPathIncludingRetired",
			"GetBookFilesIncludingRetired", "GetRetiredBookFiles", "GetRetiredVersionsFromBook"}},
	{name: "database.FileHashHolderLookup", class: forwardedRead, has: has[database.FileHashHolderLookup],
		sinks: []string{"BooksHoldingFileHash"}, methods: []string{"BooksByFileHash"}},
	// GetSeriesByID is a metafetch.Store read, promoted: DropDanglingSeriesRef
	// resolves its getter through Unwrap-looking code that accepts the wrapper
	// itself.
	{name: "database series-ref getter", class: forwardedRead, has: has[seriesRefGetter],
		sinks: []string{"DropDanglingSeriesRef"}, methods: []string{"GetSeriesByID"}},

	{name: "authorcredit.AuthoritySource", class: implementedByNoStore, has: has[authorcredit.AuthoritySource],
		sinks: []string{"AsCapability[AuthoritySource]"}},
	{name: "authorcredit.AuthorityWaiter", class: implementedByNoStore, has: has[authorcredit.AuthorityWaiter],
		sinks: []string{"AsCapability[AuthorityWaiter]"}},
	{name: "authorcredit.PersonEvidence", class: implementedByNoStore, has: has[authorcredit.PersonEvidence],
		sinks: []string{"AsCapability[PersonEvidence]"}},
}

func realStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	inner.WaitForWarmup()
	t.Cleanup(func() { _ = inner.Close() })
	return inner
}

func gatedOver(t *testing.T, inner metafetch.Store) *metafetchgate.Store {
	t.Helper()
	rows, err := metafetchgate.Rows()
	require.NoError(t, err)
	w := writertest.ForSet("books.f518b", nil, tasks.ModeLive)
	return metafetchgate.Wrap(t.Context(), inner, writer.New(w, rows))
}

// TestStoreCapabilitiesAreClassified (round 3, M1; round 2, m2): metafetchgate.Store
// embeds metafetch.Store and has no Unwrap, so database.AsCapability sees only
// the wrapper's own method set. Every optional capability the code behind it
// resolves is classified: a write is hidden (the real store has it, the wrapper
// must not), a read is forwarded (both have it), and one no store implements
// stays that way. Hiding a read was the round-2 miss: through the gate the
// apply's author resolution found no alias lookup, no title listing, no author
// history and no by-author listing, and minted a combined author row.
//
// Adding an Unwrap would turn every hidden write into a write through the
// inner store without a stamp, so the test pins its absence. A write that has
// to happen on the file-work path gets a gated method, never an Unwrap.
func TestStoreCapabilitiesAreClassified(t *testing.T) {
	inner := realStore(t)
	gated := gatedOver(t, inner)

	for _, c := range capabilities {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.class != implementedByNoStore, c.has(inner),
				"%s: the real store's offer does not match its class %q (a %q is implemented by the real store; a %q by none)",
				c.name, c.class, hiddenWrite+"/"+forwardedRead, implementedByNoStore)
			switch c.class {
			case hiddenWrite:
				require.False(t, c.has(gated), "%s resolved through the gated store: it would be written past the gate", c.name)
			case forwardedRead:
				require.True(t, c.has(gated), "%s is hidden by the gated store: its consumer would read less through the gate", c.name)
			case implementedByNoStore:
				require.False(t, c.has(gated), c.name)
			default:
				t.Fatalf("%s: unknown class %q", c.name, c.class)
			}
		})
	}

	_, unwraps := any(gated).(database.StoreUnwrapper)
	require.False(t, unwraps, "metafetchgate.Store must stay opaque (no Unwrap): see the test doc")
}

// The wrapper's methods beyond metafetch.Store are exactly the methods of the
// forwarded reads that metafetch.Store does not already have, plus TitleCacheKey: a method added to
// the wrapper (a capability offered by accident, or a forwarded read nobody
// classified) fails here.
func TestWrapperOffersNoMethodBeyondTheClassifiedReads(t *testing.T) {
	base := map[string]bool{}
	for _, m := range storeMethods() {
		base[m] = true
	}
	// TitleCacheKey is not a store capability: it names the identity the title
	// index is cached under (authorcredit.TitleCacheKeyer).
	wantSet := map[string]bool{"TitleCacheKey": true}
	for _, c := range capabilities {
		if c.class != forwardedRead {
			continue
		}
		for _, m := range c.methods {
			if !base[m] {
				wantSet[m] = true
			}
		}
	}
	var want, got []string
	for m := range wantSet {
		want = append(want, m)
	}
	ty := reflect.TypeFor[*metafetchgate.Store]()
	for i := 0; i < ty.NumMethod(); i++ {
		if m := ty.Method(i).Name; !base[m] {
			got = append(got, m)
		}
	}
	sort.Strings(want)
	sort.Strings(got)
	require.Equal(t, want, got, "the wrapper's own methods beyond metafetch.Store are not the classified forwarded reads")
}

// --- the scan: every resolution site is classified ------------------------

// consumerPackages are the packages the fetch service passes its store to and
// that resolve capabilities from it (metafetch itself, and what it hands the
// store to: organizer, tagger, fileops, authorcredit, ...), each scanned with
// every package below it. They are the one-hop set: found by listing what
// metafetch's non-test files hand the store to, and by following every
// consumer's calls into the packages that resolve capabilities (dedup, scanner,
// merge, undo, operations, catalog) to check none is given the fetch store.
var consumerPackages = []string{"metafetch", "organizer", "tagger", "fileops", "foldernames", "versionprimary", "authority", "authorcredit"}

func parseFile(t *testing.T, fset *token.FileSet, path string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)
	return f
}

func isNonTestGo(e os.DirEntry) bool {
	n := e.Name()
	return !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go")
}

// parseNonTest parses the non-test files of ONE directory.
func parseNonTest(t *testing.T, dir string) []*ast.File {
	t.Helper()
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	var out []*ast.File
	for _, e := range ents {
		if isNonTestGo(e) {
			out = append(out, parseFile(t, fset, filepath.Join(dir, e.Name())))
		}
	}
	require.NotEmpty(t, out, dir)
	return out
}

// parseTree parses the non-test files of dir and of every directory below it
// (a consumer's subpackage is as much a consumer as the package: the store is
// passed down).
func parseTree(t *testing.T, dir string) []*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	var out []*ast.File
	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if isNonTestGo(d) {
			out = append(out, parseFile(t, fset, path))
		}
		return nil
	}))
	require.NotEmpty(t, out, dir)
	return out
}

var asHelperRe = regexp.MustCompile(`^As[A-Z]`)

// callName is the called function's bare name, looking through an explicit
// instantiation.
func callName(c *ast.CallExpr) string {
	fun := c.Fun
	if ix, ok := fun.(*ast.IndexExpr); ok {
		fun = ix.X
	}
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// resolvesByHand reports whether fd resolves a capability without AsCapability:
// it walks decorators itself (it names StoreUnwrapper), or it is an As*
// resolver that type-asserts (as AsPebbleStore does).
func resolvesByHand(fd *ast.FuncDecl) bool {
	hit := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if x.Name == "StoreUnwrapper" {
				hit = true
			}
		case *ast.TypeAssertExpr, *ast.TypeSwitchStmt:
			if asHelperRe.MatchString(fd.Name.Name) {
				hit = true
			}
		}
		return !hit
	})
	return hit
}

// capabilityHelpers are the functions of the store package that resolve a
// capability from a value passed as any (see helpersOf).
func capabilityHelpers(t *testing.T) (helpers map[string]bool, pkgName string) {
	t.Helper()
	files := parseNonTest(t, "../../store")
	return helpersOf(files), files[0].Name.Name
}

// helpersOf are the top-level functions of files that resolve a capability:
// those calling AsCapability, an As* resolver, or another such helper, to a
// fixed point, and the hand-rolled resolvers they bottom out in (AsSeriesRefGetter
// walks StoreUnwrapper itself and never calls AsCapability).
func helpersOf(files []*ast.File) map[string]bool {
	top := map[string]*ast.FuncDecl{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
				top[fd.Name.Name] = fd
			}
		}
	}
	helpers := map[string]bool{}
	for name, fd := range top {
		if name != "AsCapability" && resolvesByHand(fd) {
			helpers[name] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for name, fd := range top {
			if helpers[name] || name == "AsCapability" {
				continue
			}
			hit := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					cn := callName(c)
					if cn == "AsCapability" || helpers[cn] || (asHelperRe.MatchString(cn) && top[cn] != nil) {
						hit = true
					}
				}
				return !hit
			})
			if hit {
				helpers[name] = true
				changed = true
			}
		}
	}
	return helpers
}

// storePackageName is the name file f imports the store package under: its
// import name when it has one, else the package's own name; "" when f does not
// import it.
func storePackageName(f *ast.File, pkgName string) string {
	for _, imp := range f.Imports {
		if strings.HasSuffix(strings.Trim(imp.Path.Value, `"`), "/internal/books/store") {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return pkgName
		}
	}
	return ""
}

// scanSites records, as sink names, the capability resolutions in files:
//
//   - "AsCapability[T]" for a direct resolution;
//   - the helper's name for a call of a store-package helper that resolves one
//     (under whatever name the file imports the store package, or bare for a dot
//     import);
//   - "assert X.(T)" for a bare type assertion, and "switch X.(T)" for each case
//     of a type switch: through the opaque wrapper they see exactly what
//     AsCapability sees, and store_capability.go records them as the shape of
//     earlier production bugs. Most are not on a store (a context value, a
//     metadata source); those are listed in nonStoreAssertions.
func scanSites(files []*ast.File, helpers map[string]bool, pkgName, label string, sites map[string][]string) {
	add := func(k string) { sites[k] = append(sites[k], label) }
	for _, f := range files {
		alias := storePackageName(f, pkgName)
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if ix, ok := x.Fun.(*ast.IndexExpr); ok {
					if callName(x) == "AsCapability" {
						add("AsCapability[" + types.ExprString(ix.Index) + "]")
					}
					return true
				}
				switch fun := x.Fun.(type) {
				case *ast.SelectorExpr:
					if id, ok := fun.X.(*ast.Ident); ok && alias != "" && id.Name == alias && helpers[fun.Sel.Name] {
						add(fun.Sel.Name)
					}
				case *ast.Ident:
					if alias == "." && helpers[fun.Name] {
						add(fun.Name)
					}
				}
			case *ast.TypeAssertExpr:
				if x.Type != nil { // x.(type) is the switch header, below
					add("assert " + types.ExprString(x))
				}
			case *ast.TypeSwitchStmt:
				var guard ast.Expr
				switch a := x.Assign.(type) {
				case *ast.AssignStmt:
					guard = a.Rhs[0]
				case *ast.ExprStmt:
					guard = a.X
				}
				if ta, ok := guard.(*ast.TypeAssertExpr); ok {
					for _, cl := range x.Body.List {
						for _, e := range cl.(*ast.CaseClause).List {
							add("switch " + types.ExprString(ta.X) + ".(" + types.ExprString(e) + ")")
						}
					}
				}
			}
			return true
		})
	}
}

// resolutionSites are the capability resolutions the consumer packages make on
// a store, as sink names (see scanSites).
func resolutionSites(t *testing.T) map[string][]string {
	t.Helper()
	helpers, pkgName := capabilityHelpers(t)
	sites := map[string][]string{}
	for _, pkg := range consumerPackages {
		scanSites(parseTree(t, filepath.Join("../../..", pkg)), helpers, pkgName, pkg, sites)
	}
	return sites
}

// nonStoreAssertions are the bare type assertions the consumers make that are
// not on a store: each is spelled as the scan spells it, with why it is not a
// capability resolution. A new bare assertion in a consumer package fails the
// classification until it is either a row of `capabilities` (a store capability)
// or listed here with its reason; a stale entry fails too.
var nonStoreAssertions = map[string]string{
	"assert ctx.Value(catalogStageStatsKey{}).(*CatalogStageStats)": "a context value",
	"assert ctx.Value(noBackupKey{}).(bool)":                        "a context value",
	"assert src.(metadata.ContextualSearch)":                        "a metadata source (not the store)",
	"assert src.(interface{SupportsContextSearch() bool})":          "a metadata source (not the store)",
	"assert c.(LoadReporter)":                                       "a cache, not the store",
	"assert ts.(TitleCacheKeyer)":                                   "the title source naming its cache key (authorcredit.titleCacheKey)",
	"assert providerSource().(AuthorityWaiter)":                     "the process-wide authority provider, not the store",
	"assert l.(*authority.Snapshot)":                                "an authority listing, not the store",
}

// TestEveryCapabilityTheConsumersResolveIsClassified (round 3, M1; round 4, m2):
// the capability rows above are the capabilities the consumers of the wrapper's
// store resolve, found by scanning their source rather than by remembering
// them. A resolution added to a consumer package that is in no row fails here
// until someone decides whether the wrapper hides it (a write) or forwards it
// (a read); a row whose resolution is gone is stale and fails too, which also
// proves the scan is finding things.
func TestEveryCapabilityTheConsumersResolveIsClassified(t *testing.T) {
	sites := resolutionSites(t)
	declared := map[string]string{}
	for _, c := range capabilities {
		require.NotEmpty(t, c.sinks, c.name)
		for _, s := range c.sinks {
			prev, dup := declared[s]
			require.False(t, dup, "resolution %q is in two rows: %s and %s", s, prev, c.name)
			declared[s] = c.name
		}
	}
	for s := range nonStoreAssertions {
		_, dup := declared[s]
		require.False(t, dup, "%q is both a capability row and a non-store assertion", s)
	}
	var unclassified, stale []string
	for s, pkgs := range sites {
		_, row := declared[s]
		_, other := nonStoreAssertions[s]
		if !row && !other {
			unclassified = append(unclassified, fmt.Sprintf("%s (%v)", s, slices.Compact(slices.Clone(pkgs))))
		}
	}
	for s := range declared {
		if _, ok := sites[s]; !ok {
			stale = append(stale, s)
		}
	}
	for s := range nonStoreAssertions {
		if _, ok := sites[s]; !ok {
			stale = append(stale, s)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	require.Empty(t, unclassified, "a consumer of the gated store resolves a capability that is in no row of `capabilities` (and is not a listed non-store assertion): classify it (hidden write or forwarded read)")
	require.Empty(t, stale, "a row of `capabilities` or `nonStoreAssertions` names a resolution no consumer makes any more")
}

// The scan's own behaviour, on source it is given, so each blind spot of
// round 3 stays closed: the scan is only as good as what it recognises.
func scanSource(t *testing.T, src string) map[string][]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	require.NoError(t, err)
	helpers, pkgName := capabilityHelpers(t)
	sites := map[string][]string{}
	scanSites([]*ast.File{f}, helpers, pkgName, "x", sites)
	return sites
}

func TestScanSeesEveryShapeOfResolution(t *testing.T) {
	const storeImport = `"github.com/falkcorp/audiobook-organizer/internal/books/store"`
	cases := []struct {
		name, src, want string
	}{
		{"AsCapability", `package x
import database ` + storeImport + `
func f(s any) { database.AsCapability[Foo](s) }`, "AsCapability[Foo]"},
		{"helper", `package x
import database ` + storeImport + `
func f(s any) { database.BooksHoldingFileHash(s, "h") }`, "BooksHoldingFileHash"},
		{"helper under another import name", `package x
import dbx ` + storeImport + `
func f(s any) { dbx.BooksHoldingFileHash(s, "h") }`, "BooksHoldingFileHash"},
		{"helper under the package's own name", `package x
import ` + storeImport + `
func f(s any) { ` + "DBPKG" + `.BooksHoldingFileHash(s, "h") }`, "BooksHoldingFileHash"},
		{"helper by dot import", `package x
import . ` + storeImport + `
func f(s any) { BooksHoldingFileHash(s, "h") }`, "BooksHoldingFileHash"},
		{"hand-rolled resolver", `package x
import database ` + storeImport + `
func f(s any) { database.AsSeriesRefGetter(s) }`, "AsSeriesRefGetter"},
		{"bare assertion", `package x
func f(db any) { _, _ = db.(Foo) }`, "assert db.(Foo)"},
		{"type switch", `package x
func f(db any) {
	switch v := db.(type) {
	case Foo:
		_ = v
	}
}`, "switch db.(Foo)"},
		{"type switch without a binding", `package x
func f(db any) {
	switch db.(type) {
	case Foo, Bar:
	}
}`, "switch db.(Bar)"},
	}
	_, pkgName := capabilityHelpers(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := strings.ReplaceAll(c.src, "DBPKG", pkgName)
			require.Contains(t, scanSource(t, src), c.want)
		})
	}
	// Not a resolution: a call of a same-named function from another package.
	require.Empty(t, scanSource(t, `package x
import other "example.com/other"
func f(s any) { other.BooksHoldingFileHash(s, "h") }`))
}

// The helper set includes the resolvers that never call AsCapability, and the
// ones every consumer reaches.
func TestCapabilityHelpersIncludeHandRolledResolvers(t *testing.T) {
	helpers, _ := capabilityHelpers(t)
	for _, want := range []string{"AsSeriesRefGetter", "DropDanglingSeriesRef", "AsSyncIdentityStore", "BookFileByPathIncludingRetired", "BooksHoldingFileHash"} {
		require.True(t, helpers[want], "%s is a capability helper", want)
	}
	require.False(t, helpers["AsCapability"], "AsCapability is the sink spelled AsCapability[T], not a helper")
}

// What makes a store function a helper, on source it is given: it names
// StoreUnwrapper, or it is an As* resolver that type-asserts, or it calls a
// helper; and a function that does none of those is not one.
func TestHelpersOfRecognisesEveryShapeOfResolver(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", `package x
func AsCapability[T any](s any) (T, bool) { u, ok := s.(StoreUnwrapper); _ = u; var z T; return z, ok }
func WalksByHand(s any) any { u, _ := s.(StoreUnwrapper); return u }
func AsAsserting(s any) any { v, _ := s.(Foo); return v }
func AsSwitching(s any) any { switch v := s.(type) { case Foo: return v }; return nil }
func ViaCapability(s any) { AsCapability[Foo](s) }
func ViaHelper(s any) { WalksByHand(s) }
func ViaAsHelper(s any) { AsAsserting(s) }
func Plain(s any) any { return s }
func notAs(s any) any { v, _ := s.(Foo); return v }
`, 0)
	require.NoError(t, err)
	got := helpersOf([]*ast.File{f})
	for _, want := range []string{"WalksByHand", "AsAsserting", "AsSwitching", "ViaCapability", "ViaHelper", "ViaAsHelper"} {
		require.True(t, got[want], "%s resolves a capability", want)
	}
	for _, not := range []string{"AsCapability", "Plain", "notAs"} {
		require.False(t, got[not], "%s does not resolve one by itself", not)
	}
}

// A consumer's subpackage is scanned with it.
func TestParseTreeDescendsIntoSubpackages(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "testdata"), 0o755))
	for path, pkg := range map[string]string{"a.go": "a", "sub/b.go": "b", "sub/deeper/c.go": "c", "testdata/d.go": "d", "sub/e_test.go": "e"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte("package "+pkg+"\n"), 0o644))
	}
	var got []string
	for _, f := range parseTree(t, dir) {
		got = append(got, f.Name.Name)
	}
	sort.Strings(got)
	require.Equal(t, []string{"a", "b", "c"}, got, "subpackages are scanned; testdata and test files are not")

	// And the real consumers: versionprimary's subpackage is among what is scanned.
	var seen []string
	for _, f := range parseTree(t, "../../../versionprimary") {
		seen = append(seen, f.Name.Name)
	}
	require.Contains(t, seen, "vptest")
}

// --- the forwarded reads reach the inner store ----------------------------

// capInner is a metafetch.Store offering the forwarded read capabilities, each
// recording its arguments and returning a sentinel or the injected error.
type capInner struct {
	metafetch.Store // never called
	fail            error
	calls           []string
	args            []any
	author          *database.Author
	series          []database.Series
	hist            []database.MetadataChangeRecord
	books           []database.BookCore
	file            *database.BookFile
	files           []database.BookFile
	filesCore       []database.BookFileCore
	versions        []database.BookVersion
	holders         []*database.Book
}

func (c *capInner) rec(m string, a ...any) { c.calls = append(c.calls, m); c.args = a }

func (c *capInner) FindAuthorByAlias(n string) (*database.Author, error) {
	c.rec("FindAuthorByAlias", n)
	return c.author, c.fail
}
func (c *capInner) GetAllSeries() ([]database.Series, error) {
	c.rec("GetAllSeries")
	return c.series, c.fail
}
func (c *capInner) GetMetadataChangeHistory(b, f string, l int) ([]database.MetadataChangeRecord, error) {
	c.rec("GetMetadataChangeHistory", b, f, l)
	return c.hist, c.fail
}
func (c *capInner) GetBooksByAuthorIDWithRoleCore(id int) ([]database.BookCore, error) {
	c.rec("GetBooksByAuthorIDWithRoleCore", id)
	return c.books, c.fail
}
func (c *capInner) GetBookFilesIncludingRetired(id string) ([]database.BookFile, error) {
	c.rec("GetBookFilesIncludingRetired", id)
	return c.files, c.fail
}
func (c *capInner) GetRetiredBookFiles(id string) ([]database.BookFile, error) {
	c.rec("GetRetiredBookFiles", id)
	return c.files, c.fail
}
func (c *capInner) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	c.rec("GetAllBookFilesCoreIncludingRetired")
	return c.filesCore, c.fail
}
func (c *capInner) GetRetiredVersionsFromBook(id string) ([]database.BookVersion, error) {
	c.rec("GetRetiredVersionsFromBook", id)
	return c.versions, c.fail
}
func (c *capInner) GetBookFileByPIDIncludingRetired(pid string) (*database.BookFile, error) {
	c.rec("GetBookFileByPIDIncludingRetired", pid)
	return c.file, c.fail
}
func (c *capInner) GetBookFileByPathIncludingRetired(p string) (*database.BookFile, error) {
	c.rec("GetBookFileByPathIncludingRetired", p)
	return c.file, c.fail
}
func (c *capInner) BooksByFileHash(h string) ([]*database.Book, error) {
	c.rec("BooksByFileHash", h)
	return c.holders, c.fail
}

// readCall is one forwarded read: the wrapper call, the arguments it makes, and
// the result it must return.
type readCall struct {
	method string
	call   func(g *metafetchgate.Store) (result any, err error)
	args   []any
	result func(c *capInner) any
}

func readCalls() []readCall {
	return []readCall{
		{"FindAuthorByAlias", func(g *metafetchgate.Store) (any, error) { return g.FindAuthorByAlias("a1") }, []any{"a1"}, func(c *capInner) any { return c.author }},
		{"GetAllSeries", func(g *metafetchgate.Store) (any, error) { return g.GetAllSeries() }, nil, func(c *capInner) any { return c.series }},
		{"GetMetadataChangeHistory", func(g *metafetchgate.Store) (any, error) { return g.GetMetadataChangeHistory("b1", "author", 7) }, []any{"b1", "author", 7}, func(c *capInner) any { return c.hist }},
		{"GetBooksByAuthorIDWithRoleCore", func(g *metafetchgate.Store) (any, error) { return g.GetBooksByAuthorIDWithRoleCore(42) }, []any{42}, func(c *capInner) any { return c.books }},
		{"GetBookFilesIncludingRetired", func(g *metafetchgate.Store) (any, error) { return g.GetBookFilesIncludingRetired("b1") }, []any{"b1"}, func(c *capInner) any { return c.files }},
		{"GetRetiredBookFiles", func(g *metafetchgate.Store) (any, error) { return g.GetRetiredBookFiles("h1") }, []any{"h1"}, func(c *capInner) any { return c.files }},
		{"GetAllBookFilesCoreIncludingRetired", func(g *metafetchgate.Store) (any, error) { return g.GetAllBookFilesCoreIncludingRetired() }, nil, func(c *capInner) any { return c.filesCore }},
		{"GetRetiredVersionsFromBook", func(g *metafetchgate.Store) (any, error) { return g.GetRetiredVersionsFromBook("s1") }, []any{"s1"}, func(c *capInner) any { return c.versions }},
		{"GetBookFileByPIDIncludingRetired", func(g *metafetchgate.Store) (any, error) { return g.GetBookFileByPIDIncludingRetired("pid1") }, []any{"pid1"}, func(c *capInner) any { return c.file }},
		{"GetBookFileByPathIncludingRetired", func(g *metafetchgate.Store) (any, error) { return g.GetBookFileByPathIncludingRetired("/lib/x.m4b") }, []any{"/lib/x.m4b"}, func(c *capInner) any { return c.file }},
		{"BooksByFileHash", func(g *metafetchgate.Store) (any, error) { return g.BooksByFileHash("h") }, []any{"h"}, func(c *capInner) any { return c.holders }},
	}
}

func newCapInner() *capInner {
	return &capInner{
		author:    &database.Author{ID: 1, Name: "Bruce Wayne"},
		series:    []database.Series{{ID: 3, Name: "The Long Halloween"}},
		hist:      []database.MetadataChangeRecord{{BookID: "b1"}},
		books:     []database.BookCore{{ID: "b9"}},
		file:      &database.BookFile{ID: "f1"},
		files:     []database.BookFile{{ID: "f2"}},
		filesCore: []database.BookFileCore{{ID: "f3"}},
		versions:  []database.BookVersion{{ID: "v1"}},
		holders:   []*database.Book{{ID: "b2"}},
	}
}

// Each forwarded read calls the SAME-NAMED inner method once, with its
// arguments, and returns the inner store's result; and returns the inner
// store's error, never an empty answer in its place.
func TestForwardedReadsReachTheInnerStore(t *testing.T) {
	for _, rc := range readCalls() {
		t.Run(rc.method, func(t *testing.T) {
			inner := newCapInner()
			got, err := rc.call(gatedOver(t, inner))
			require.NoError(t, err)
			require.Equal(t, []string{rc.method}, inner.calls)
			require.Equal(t, rc.args, append([]any(nil), inner.args...))
			require.Equal(t, rc.result(inner), got)

			boom := errors.New("inner failed")
			inner = newCapInner()
			inner.fail = boom
			_, err = rc.call(gatedOver(t, inner))
			require.ErrorIs(t, err, boom, "%s swallowed the inner error", rc.method)
		})
	}
}

// bareInner is a metafetch.Store with none of the optional capabilities.
type bareInner struct{ metafetch.Store }

// Over an inner store without a capability, a forwarded read answers as the
// consumer already handles a miss: the optional author-resolution lookups say
// "nothing", the retired-row reader and the holder lookup fail closed with the
// error the database helpers return for a store without them.
func TestForwardedReadsOverAStoreWithoutTheCapabilityAreAMiss(t *testing.T) {
	g := gatedOver(t, bareInner{})

	a, err := g.FindAuthorByAlias("x")
	require.NoError(t, err)
	require.Nil(t, a)
	series, err := g.GetAllSeries()
	require.NoError(t, err)
	require.Empty(t, series)
	hist, err := g.GetMetadataChangeHistory("b", "author", 1)
	require.NoError(t, err)
	require.Empty(t, hist)
	books, err := g.GetBooksByAuthorIDWithRoleCore(1)
	require.NoError(t, err)
	require.Empty(t, books)

	for _, rc := range readCalls() {
		switch rc.method {
		case "GetBookFilesIncludingRetired", "GetRetiredBookFiles", "GetAllBookFilesCoreIncludingRetired",
			"GetRetiredVersionsFromBook", "GetBookFileByPIDIncludingRetired", "GetBookFileByPathIncludingRetired":
			_, err := rc.call(g)
			require.ErrorIs(t, err, database.ErrRetiredReaderUnavailable, rc.method)
		case "BooksByFileHash":
			_, err := rc.call(g)
			require.ErrorIs(t, err, database.ErrNoFileHashHolderLookup)
		}
	}
	// And through the helpers the consumers call, as the consumers call them.
	_, err = database.BookFileByPathIncludingRetired(g, "/lib/x.m4b")
	require.ErrorIs(t, err, database.ErrRetiredReaderUnavailable)
	_, err = database.BooksHoldingFileHash(g, "h")
	require.ErrorIs(t, err, database.ErrNoFileHashHolderLookup)
	alias, err := authorcredit.FindByAlias(g, "x")
	require.NoError(t, err)
	require.Nil(t, alias)
}

// --- the title index cache ------------------------------------------------

// keyedInner is an inner store that names its own title cache key and is a
// TitleSource, so the wrapper must pass that key on rather than name the inner
// store.
type keyedInner struct {
	metafetch.Store
	key any
}

func (k keyedInner) TitleCacheKey() authorcredit.TitleKey     { return authorcredit.TitleKeyOf(k.key) }
func (k keyedInner) GetAllSeries() ([]database.Series, error) { return nil, nil }

// layeredInner is a decorator with an Unwrap and no GetAllSeries: on the bare
// decorator authorcredit's TitleSource resolves through Unwrap to the layer
// below, so that layer is the cache entry.
type layeredInner struct {
	metafetch.Store
	below any
}

func (l layeredInner) Unwrap() database.Store { return l.below.(database.Store) }

// requireSameKey compares by ==: testify's Equal would DeepEqual the stores the
// keys point at, which says nothing about identity.
func requireSameKey(t *testing.T, want, got authorcredit.TitleKey, msg string) {
	t.Helper()
	require.True(t, want == got, msg)
}

// The wrapper names the key of the layer a BARE store resolves TitleSource at
// (round 4, m1 and n2): the inner store when it is the TitleSource, that
// layer's own key when it names one, the layer below for a decorator that
// unwraps, and itself only when there is no inner store.
func TestWrapperNamesTheBareStoresTitleLayerAsItsTitleCacheKey(t *testing.T) {
	inner := realStore(t)
	requireSameKey(t, authorcredit.TitleKeyOf(inner), gatedOver(t, inner).TitleCacheKey(), "the key is the inner store")

	own := &struct{ n int }{}
	requireSameKey(t, authorcredit.TitleKeyOf(own), gatedOver(t, keyedInner{key: own}).TitleCacheKey(), "an inner store's own key wins")

	// n2: a decorator over the real store that unwraps, with no GetAllSeries of
	// its own. The bare decorator's TitleSource resolves at the real store.
	dec := layeredInner{Store: inner, below: inner}
	_, ok := database.AsCapability[authorcredit.TitleSource](dec)
	require.True(t, ok, "the bare decorator resolves TitleSource through Unwrap")
	requireSameKey(t, authorcredit.TitleKeyOf(inner), gatedOver(t, dec).TitleCacheKey(),
		"a decorator in front of the layer that is the TitleSource must not change the entry")
	require.False(t, authorcredit.TitleKeyOf(dec) == gatedOver(t, dec).TitleCacheKey(), "the key must not be the decorator")

	// No layer is a TitleSource: the wrapper synthesizes one from the inner
	// store's reads, cached under the inner store.
	requireSameKey(t, authorcredit.TitleKeyOf(bareInnerPtr), gatedOver(t, bareInnerPtr).TitleCacheKey(), "no TitleSource layer: the inner store")

	g := gatedOver(t, nil)
	requireSameKey(t, authorcredit.TitleKeyOf(g), g.TitleCacheKey(), "no inner store: the wrapper itself")
}

var bareInnerPtr = &bareInner{}

// The key is opaque (round 4, m1): returning the inner store from an exported
// method is an Unwrap under another name. The key satisfies no store interface
// and nothing outside authorcredit reads the store back from it.
func TestTitleCacheKeyHandsBackNoStore(t *testing.T) {
	inner := realStore(t)
	var k any = gatedOver(t, inner).TitleCacheKey()
	_, isMetafetch := k.(metafetch.Store)
	require.False(t, isMetafetch, "the key satisfies metafetch.Store: it can write through the inner store")
	_, isStore := k.(database.Store)
	require.False(t, isStore, "the key satisfies database.Store")
	_, unwraps := k.(database.StoreUnwrapper)
	require.False(t, unwraps, "the key unwraps")
	require.Zero(t, reflect.TypeOf(k).NumMethod(), "a key method could read the store back")
	_, same := k.(*database.PebbleStore)
	require.False(t, same)
}

// The no-Unwrap pin, for the shape TitleCacheKey once had (round 4, m1): no
// method of the wrapper may return the inner store, or anything that could be
// one. An output that is an empty interface (the store boxed as `any`), or that
// satisfies a store interface or unwraps, is a handle on the inner store.
func TestNoMethodHandsBackTheInnerStore(t *testing.T) {
	storeIfaces := []reflect.Type{
		reflect.TypeFor[metafetch.Store](),
		reflect.TypeFor[database.Store](),
		reflect.TypeFor[database.StoreUnwrapper](),
	}
	var bad []string
	ty := reflect.TypeFor[*metafetchgate.Store]()
	for i := range ty.NumMethod() {
		m := ty.Method(i)
		for j := range m.Type.NumOut() {
			out := m.Type.Out(j)
			if out.Kind() == reflect.Interface && out.NumMethod() == 0 {
				bad = append(bad, fmt.Sprintf("%s returns %s (an empty interface can box the store)", m.Name, out))
			}
			for _, si := range storeIfaces {
				if out.Implements(si) {
					bad = append(bad, fmt.Sprintf("%s returns %s, which is a %s", m.Name, out, si.Name()))
				}
			}
			if _, has := out.MethodByName("Unwrap"); has {
				bad = append(bad, fmt.Sprintf("%s returns %s, which has Unwrap", m.Name, out))
			}
		}
	}
	// Fields are a handle as much as methods are: an exported embedded
	// metafetch.Store made `gated.Store.CreateBook(...)` a write past the gate
	// with no method of the wrapper in the way (F5-18b round 4, minor). Any
	// exported field, however typed, is a way for a caller to name it.
	sty := reflect.TypeFor[metafetchgate.Store]()
	for _, f := range reflect.VisibleFields(sty) {
		if f.IsExported() {
			bad = append(bad, fmt.Sprintf("field %s (%s) is exported: a caller can reach it", f.Name, f.Type))
		}
	}
	require.Empty(t, bad, "the wrapper must not hand back the inner store (an Unwrap under another name)")
}

// A wrapper is built per run. Resolving a credit through one wrapper per run
// leaves ONE title index cached, the entry the bare store would have had, not
// one per run (each a whole library's titles, never evicted).
func TestTitleIndexCacheDoesNotGrowAcrossGatedRuns(t *testing.T) {
	authorcredit.ResetTitleCache()
	t.Cleanup(authorcredit.ResetTitleCache)
	inner := realStore(t)
	for _, n := range []string{"Bruce Wayne", "Dick Grayson"} {
		_, err := inner.CreateAuthor(n)
		require.NoError(t, err)
	}
	const credit = "Bruce Wayne, Dick Grayson"
	for range 12 {
		got, err := authorcredit.Resolve(gatedOver(t, inner), credit, authorcredit.PrepareGate)
		require.NoError(t, err)
		require.Len(t, got, 2)
	}
	require.Equal(t, 1, authorcredit.TitleCacheLen(), "a title index per gated run was cached")

	// And it is the bare store's own entry: the bare store reuses it.
	before := authorcredit.TitleCacheLen()
	_, err := authorcredit.Resolve(inner, credit, authorcredit.PrepareGate)
	require.NoError(t, err)
	require.Equal(t, before, authorcredit.TitleCacheLen())
}
