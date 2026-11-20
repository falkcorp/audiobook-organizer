// file: internal/plugins/maintenance/deps_jobs_capabilities_test.go
// version: 0.1.0
// guid: 2e9c6a41-7b3d-4f85-a0c2-d18f5e4b7a93
// last-edited: 2026-11-20

package maintenance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// capClass is what the jobs' store does with a capability the jobs resolve.
type capClass string

const (
	// capImplemented: the wrapper satisfies the whole interface, every method
	// forwarded (a read), gated (a write with a row) or refused (a write without
	// one). The test compares the interface's method set, read from the source
	// by the scan, with the wrapper's, signature by signature: a copy of the
	// interface written by hand here would be one more thing to drift.
	capImplemented capClass = "implemented"
	// capNoStore: the thing asserted to is not a store (a logger, a progress
	// reporter, a path checker, a context value), or no store offers it (an
	// evidence source only a test double has). The real store must not have it
	// either: if one starts to, this fails until the capability is classified as
	// a write or a read. The wrapper must not have it.
	capNoStore capClass = "implemented by no store"
	// capConcrete: a concrete store type. The wrapper is not one and has no
	// Unwrap, so the resolution yields nothing on it; the consumer's other branch
	// is the wrapper's own capability.
	capConcrete capClass = "concrete store type"
)

type capRow struct {
	class capClass
	why   string
}

// jobCapabilityRows is every optional capability the jobs reach, keyed by the
// scan's site key. TestEveryCapabilityTheJobsResolveIsClassified fails on a site
// in no row and on a row no site matches.
var jobCapabilityRows = map[string]capRow{
	// merge.Service and the merge helpers the jobs hand their store to
	"AsCapability[merge.ExternalIDReassigner]":                             {capImplemented, "merge-chapter-groups: ReassignExternalIDs, gated"},
	"AsCapability[merge.JournalPointerWriter]":                             {capImplemented, "CreateOperationChange, gated"},
	"AsCapability[merge.UserProgressMerger]":                               {capImplemented, "the chapter merge's user-progress carry"},
	"AsCapability[merge.opChangeScanner]":                                  {capImplemented, "backfill-merge-undo-pointers: ScanOperationChanges, a read"},
	"AsCapability[merge.pendingRepairScanner]":                             {capImplemented, "ScanPrefix, DeleteRaw (by key family)"},
	"AsCapability[merge.rawDeleter]":                                       {capImplemented, "GetRaw, DeleteRaw (by key family)"},
	"AsCapability[merge.rawGetter]":                                        {capImplemented, "GetRaw"},
	"AsCapability[merge.syncMergeClearer]":                                 {capImplemented, "ClearSyncMerge: no row, refused"},
	"AsCapability[merge.syncRedirectClearer]":                              {capImplemented, "ClearSyncRedirect: no row, refused"},
	"assert merge.bookReader":                                              {capImplemented, "GetBookByID"},
	"AsCapability[store.StrictUserLister]":                                 {capImplemented, "ListUsersStrict, a read"},
	"AsCapability[store.UserBookStateDeleter]":                             {capImplemented, "DeleteUserBookState: no row, refused"},
	"AsCapability[store.UserPositionReplacer]":                             {capImplemented, "ReplaceUserPositions: no row, refused"},
	"AsCapability[store.IdentDirtyMarker]":                                 {capImplemented, "MarkIdentDirty: no row, refused (MarkIdentDirtyBestEffort logs and goes on)"},
	"AsCapability[store.retiredVersionProbe]":                              {capImplemented, "HasRetiredVersions, a read"},
	"AsCapability[store.BookmarkCopier]":                                   {capImplemented, "CopyBookmarkIfAbsent, gated"},
	"AsCapability[store.BookmarkStore]":                                    {capImplemented, "CreateBookmark gated; UpdateBookmarkTitle and DeleteBookmark have no row, refused"},
	"AsCapability[store.SyncFileStore]":                                    {capImplemented, "MintOrGetSyncFileID and RepointSyncFileToBook gated; the other two writes have no row, refused"},
	"AsCapability[store.SyncIdentityStore]":                                {capImplemented, "MintOrGetSyncID and RecordSyncMerge gated; RepointSyncItem has no row, refused"},
	"AsCapability[store.UserPositionTimestampWriter]":                      {capImplemented, "SetUserPositionAt, gated"},
	"AsCapability[interface{GetUserByID(id string) (*store.User, error)}]": {capImplemented, "ResolveMergedUser: GetUserByID, a read"},

	// the chapter-group job's own narrow interfaces, and the dedup store it
	// asserts its store to
	"assert dedup.Store":                {capImplemented, "merge-chapter-groups asserts its store to the whole dedup.Store"},
	"assert jobs.chapterActiveOpsStore": {capImplemented, "ListActiveOperationsV2, a read"},
	"assert jobs.chapterBlockerStore":   {capImplemented, "merge.UserProgressMerger + ListUserPlaylists + GetBookAuthors"},
	"assert jobs.chapterDirLister":      {capImplemented, "LiveBookPathsUnderDir, a read"},

	// the other jobs' capabilities
	"AsCapability[jobs.aggregatesBackfillMarker]":   {capImplemented, "recompute-book-aggregates"},
	"AsCapability[jobs.hbDeferredStore]":            {capImplemented, "backfill-file-hashes"},
	"assert jobs.hbDeferredStore":                   {capImplemented, "backfill-file-hashes"},
	"AsCapability[store.BookCreditsModifier]":       {capImplemented, "refetch-missing-authors: ModifyBookCredits gated, ModifyBookNarrators refused (no row)"},
	"AsCapability[store.SeriesUnreferencedDeleter]": {capImplemented, "cleanup-series"},
	"AsCapability[store.SeriesDeleteReadiness]":     {capImplemented, "cleanup-series"},
	"AsCapability[store.SeriesBookRefStore]":        {capImplemented, "cleanup-series: GetAllSeriesBookRefCounts, a read"},
	"AsCapability[store.SeriesMembershipStore]":     {capImplemented, "cleanup-series: GetBooksBySeriesIDsAllVersions, a read"},
	"AsCapability[store.SnapshotPrunePlanner]":      {capImplemented, "prune-book-snapshots"},
	"AsCapability[jobs.metricsSweeper]":             {capImplemented, "sweep-pebble-metrics-ttl: SweepExpiredMetrics, gated"},
	"AsCapability[store.RetiredBookFileReader]":     {capImplemented, "the retired-row helpers (BookFileByPathIncludingRetired and the rest): six reads"},
	"AsCapability[store.FileProvenanceRecorder]":    {capImplemented, "scan-composer-tags: AppendFileEvent, gated"},
	"assert fileops.BookFileHashRecorder":           {capImplemented, "scan-composer-tags: UpdateBookFileHashes gated, GetBookFileByPath"},
	"assert maintenance.jobBookFileWriter":          {capImplemented, "backfill-file-hashes: six gated book_file writes"},
	"assert store.seriesRefGetter":                  {capImplemented, "DropDanglingSeriesRef: GetSeriesByID"},
	"AsCapability[authorcredit.AliasFinder]":        {capImplemented, "refetch-missing-authors: FindAuthorByAlias, a read"},
	"AsCapability[authorcredit.AuthorBooksSource]":  {capImplemented, "refetch-missing-authors: GetBooksByAuthorIDWithRoleCore, a read"},
	"AsCapability[authorcredit.TitleSource]":        {capImplemented, "refetch-missing-authors: GetAllSeries, GetAllBooksCore"},
	"assert authorcredit.TitleCacheKeyer":           {capImplemented, "TitleCacheKey names the cache entry a per-run store would otherwise leak a title index under"},

	// the sweep job: its fallback resolves the Pebble store, which the wrapper
	// does not carry, after resolving the sweeper the wrapper does
	"AsCapability[*store.PebbleStore]": {capConcrete, "sweep-pebble-metrics-ttl falls back to it; the wrapper offers the narrow SweepExpiredMetrics instead"},

	// not stores, or offered by no store
	"AsCapability[authorcredit.AuthoritySource]": {capNoStore, "only a test double carries it (metafetchgate classifies it the same)"},
	"AsCapability[authorcredit.PersonEvidence]":  {capNoStore, "only a test double carries it (metafetchgate classifies it the same)"},
	"assert bookfileaudio.warnDemoter":           {capNoStore, "asserted on the logger"},
	"assert deluge.protectionClassifier":         {capNoStore, "asserted on the path checker"},
	"assert jobs.checkpointer":                   {capNoStore, "a context value"},
	"assert maintenance.ProgressSeeder":          {capNoStore, "asserted on the progress reporter"},
	"assert registry.LivenessToucher":            {capNoStore, "asserted on the progress reporter"},
	"assert tagger.LoadReporter":                 {capNoStore, "asserted on the path checker"},
}

// --- method sets, compared by name and signature ---------------------------

var (
	qualifierRe = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.`)
	anyRe       = regexp.MustCompile(`\bany\b`)
	byteRe      = regexp.MustCompile(`\bbyte\b`)
	runeRe      = regexp.MustCompile(`\brune\b`)
)

// normType makes go/types' and reflect's spellings of a type agree: no spaces,
// no package qualifiers (the method name and the shape are what a type
// assertion's answer depends on, and the same name in two packages cannot
// both be in one signature of one interface), interface{} for any, and the
// types byte and rune spelled as reflect spells them (uint8, int32).
func normType(s string) string {
	s = stripParamNames(s)
	s = strings.ReplaceAll(s, " ", "")
	s = anyRe.ReplaceAllString(s, "interface{}")
	s = byteRe.ReplaceAllString(s, "uint8")
	s = runeRe.ReplaceAllString(s, "int32")
	return qualifierRe.ReplaceAllString(s, "")
}

// paramNameRe is a parameter name inside a function type's parameter list as
// go/types prints it ("func(book *Book, credits *Edit) error"); reflect prints the
// same type without them.
var paramNameRe = regexp.MustCompile(`([(,] ?)([A-Za-z_][A-Za-z0-9_]*) ([^ ,)])`)

// typeKeywords are words that can follow "(" or "," and a space without being
// a parameter name.
var typeKeywords = map[string]bool{"chan": true, "func": true, "map": true, "interface": true, "struct": true}

func stripParamNames(s string) string {
	for {
		n := paramNameRe.ReplaceAllStringFunc(s, func(m string) string {
			g := paramNameRe.FindStringSubmatch(m)
			if typeKeywords[g[2]] {
				return m
			}
			return g[1] + g[3]
		})
		if n == s {
			return s
		}
		s = n
	}
}

func sigString(params, results []string, variadic bool) string {
	if variadic && len(params) > 0 {
		last := params[len(params)-1]
		params[len(params)-1] = "..." + strings.TrimPrefix(last, "[]")
	}
	out := "(" + strings.Join(params, ",") + ")"
	switch len(results) {
	case 0:
	case 1:
		out += results[0]
	default:
		out += "(" + strings.Join(results, ",") + ")"
	}
	return out
}

// capIfaceMethods is the complete method set of an interface type, name to
// normalised signature.
func capIfaceMethods(t *testing.T, ty types.Type) map[string]string {
	t.Helper()
	it, ok := ty.Underlying().(*types.Interface)
	require.True(t, ok, "%s is not an interface", ty)
	out := map[string]string{}
	for i := 0; i < it.NumMethods(); i++ {
		m := it.Method(i)
		sig := m.Type().(*types.Signature)
		var ps, rs []string
		for j := 0; j < sig.Params().Len(); j++ {
			ps = append(ps, normType(types.TypeString(sig.Params().At(j).Type(), qual)))
		}
		for j := 0; j < sig.Results().Len(); j++ {
			rs = append(rs, normType(types.TypeString(sig.Results().At(j).Type(), qual)))
		}
		out[m.Name()] = sigString(ps, rs, sig.Variadic())
	}
	return out
}

// concreteMethods is the exported method set of a concrete type, name to
// normalised signature (the receiver is not part of it).
func concreteMethods(rt reflect.Type) map[string]string {
	out := map[string]string{}
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		var ps, rs []string
		for j := 1; j < m.Type.NumIn(); j++ {
			ps = append(ps, normType(m.Type.In(j).String()))
		}
		for j := 0; j < m.Type.NumOut(); j++ {
			rs = append(rs, normType(m.Type.Out(j).String()))
		}
		out[m.Name] = sigString(ps, rs, m.Type.IsVariadic())
	}
	return out
}

// missingFrom lists the methods of want that have is no matching method of.
func missingFrom(want, have map[string]string) []string {
	var out []string
	for name, sig := range want {
		if have[name] != sig {
			out = append(out, fmt.Sprintf("%s%s (has %q)", name, sig, have[name]))
		}
	}
	sort.Strings(out)
	return out
}

// --- what the wrapper's own source says each method does -------------------

// wrapperMethod is a method of *booksJobsStore as written in deps_jobs*.go:
// whether its body calls the gate, and with what label.
type wrapperMethod struct {
	gated bool
	label string
}

// wrapperSource reads the wrapper's declarations. The label a gated method
// passes is a string literal, so a method that gates under another method's row
// (or a computed one) is visible here; the raw writes compute theirs from the
// key and are checked by their own test.
func wrapperSource(t *testing.T) map[string]wrapperMethod {
	t.Helper()
	files, err := filepath.Glob("deps_jobs*.go")
	require.NoError(t, err)
	out := map[string]wrapperMethod{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		af, err := parser.ParseFile(fset, f, src, 0)
		require.NoError(t, err)
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil {
				continue
			}
			star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); !ok || id.Name != "booksJobsStore" {
				continue
			}
			wm := wrapperMethod{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := c.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "do" {
					return true
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); !ok || inner.Sel.Name != "gate" {
					return true
				}
				wm.gated = true
				if len(c.Args) >= 2 {
					if lit, ok := c.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						wm.label, _ = strconv.Unquote(lit.Value)
					}
				}
				return true
			})
			out[fd.Name.Name] = wm
		}
	}
	return out
}

// jobsCapabilityReads is every method of the wrapper that is neither promoted
// from jobsReads, nor gated, nor refused, nor TitleCacheKey: the capability
// reads it forwards. It is a statement that each of them writes nothing. The
// checks around it: none has a row in the shared table, none calls the gate, and
// a method that is not in the list and is not gated fails the test below, so a
// forwarded method nobody classified cannot appear.
var jobsCapabilityReads = []string{
	"AuditMergeJournalSnapshots", "CheckSnapshotPins", "FindAuthorByAlias", "GetAllBookFilesCoreIncludingRetired",
	"GetAllSeriesBookRefCounts", "GetBookAlternativeTitles", "GetBookAtVersion", "GetBookFileByAcoustID",
	"GetBookFileByPIDIncludingRetired", "GetBookFileByPathIncludingRetired", "GetBookFilesIncludingRetired",
	"GetBookSnapshots", "GetBooksByAuthorIDCore", "GetBooksByAuthorIDWithRoleCore", "GetBooksByMetadataSourceHash",
	"GetBooksBySeriesIDsAllVersions", "GetDuplicateBooks", "GetDuplicateBooksByMetadataCore", "GetFolderDuplicatesCore",
	"GetRetiredBookFiles", "GetRetiredVersionsFromBook", "GetSyncFileID", "GetSyncIDForBook", "GetUserByID",
	"HasRetiredVersions", "IsBookAggregatesBackfillDone", "ListActiveOperationsV2", "ListBookmarks",
	"ListBookmarksForUser", "ListSyncAliases", "ListSyncFilesForBook", "ListUserPlaylists", "ListUsersStrict",
	"LiveBookPathsUnderDir", "PlanBookSnapshotPrune", "ResolveSyncItem", "ScanOperationChanges", "SeriesDeleteReady",
}

// TestJobsStoreEveryMethodIsGatedRefusedOrAForwardedRead: the wrapper's source,
// its label lists and the shared table agree. A method that calls the gate does
// so under its own name (the raw writes under a key family), and is in exactly
// one of jobsWriteLabels (has a row) and jobsRefusedLabels (has none); a method
// that does not call the gate is promoted from jobsReads, is TitleCacheKey, or
// is in jobsCapabilityReads, and has no row.
func TestJobsStoreEveryMethodIsGatedRefusedOrAForwardedRead(t *testing.T) {
	src := wrapperSource(t)
	reads := map[string]bool{}
	for m := range reflect.TypeFor[jobsReads]().Methods() {
		reads[m.Name] = true
	}
	capReads := map[string]bool{}
	for _, n := range jobsCapabilityReads {
		capReads[n] = true
	}
	gated := map[string]bool{}
	for _, l := range jobsWriteLabels {
		gated[l] = true
	}
	refused := map[string]bool{}
	for _, l := range jobsRefusedLabels {
		refused[l] = true
	}

	wt := reflect.TypeFor[*booksJobsStore]()
	for i := 0; i < wt.NumMethod(); i++ {
		name := wt.Method(i).Name
		decl, declared := src[name]
		switch {
		case !declared:
			require.True(t, reads[name], "%s is promoted but is not a jobsReads method", name)
		case decl.gated:
			if name == "SetRaw" || name == "DeleteRaw" {
				continue
			}
			require.Equal(t, name, decl.label, "%s gates under another method's row", name)
			require.NotEqual(t, gated[name], refused[name], "%s must be in exactly one of jobsWriteLabels and jobsRefusedLabels", name)
			// A row may be empty (the aggregates sentinel runs unfenced); what the
			// gate looks up is whether the method has one at all.
			_, hasRow := t6WriterResources[name]
			require.Equal(t, gated[name], hasRow, "%s: gated if and only if the shared table has a row for it", name)
		case name == "TitleCacheKey":
		default:
			require.True(t, capReads[name], "%s is a method of the wrapper that does not call the gate and is not a classified read: "+
				"add it to jobsCapabilityReads if it writes nothing, or gate it", name)
			require.NotContains(t, t6WriterResources, name, "%s is classified as a read but has a row", name)
			require.False(t, gated[name] || refused[name], "%s is both a read and a write label", name)
		}
	}
	for _, n := range jobsCapabilityReads {
		_, ok := wt.MethodByName(n)
		require.True(t, ok, "jobsCapabilityReads names %s, which the wrapper does not have", n)
		require.False(t, reads[n], "%s is already a jobsReads method", n)
	}
	for l := range gated {
		if strings.Contains(l, "/") {
			continue
		}
		_, ok := wt.MethodByName(l)
		require.True(t, ok, "jobsWriteLabels names %s, which the wrapper does not have", l)
	}
}

// TestEveryCapabilityTheJobsResolveIsClassified is the completeness proof for
// the jobs' store. The scan (deps_jobs_capscan_test.go) walks, per job, the
// functions its Run reaches and records every place code resolves an optional
// capability from a value: a type assertion to an interface, AsCapability[T],
// and the store package's As* and by-hand resolvers. Each site must be in
// jobCapabilityRows, and each row must still be a site. For a capability the
// wrapper implements, the interface's method set is taken from the source and
// compared with the wrapper's, so a method added to one of those interfaces, or
// a new interface a job starts to assert to, fails here instead of becoming a
// capability a job finds missing at runtime.
//
// What the scan sees is what the jobs package's write oracle sees: closures and
// in-module callees, not a call through an interface. merge.Service is entered
// through interfaces, so the whole merge package is scanned as well, reached or
// not.
func TestEveryCapabilityTheJobsResolveIsClassified(t *testing.T) {
	found := map[string]*siteInfo{}
	reachedBy := map[string][]string{}
	for job, sites := range jobSites(t) {
		for k, si := range sites {
			if found[k] == nil {
				found[k] = si
			}
			reachedBy[k] = append(reachedBy[k], job)
		}
	}
	for k := range reachedBy {
		sort.Strings(reachedBy[k])
	}

	var unclassified, stale []string
	for _, k := range scanSortedKeys(found) {
		if _, ok := jobCapabilityRows[k]; !ok {
			unclassified = append(unclassified, fmt.Sprintf("%s (reached by %v, first in %s)", k, reachedBy[k], found[k].where))
		}
	}
	for _, k := range scanSortedKeys(jobCapabilityRows) {
		if found[k] == nil {
			stale = append(stale, k)
		}
	}
	require.Empty(t, unclassified, "capability resolutions no row classifies: add each to jobCapabilityRows, "+
		"implementing it on booksJobsStore first if the jobs need it through the wrapper")
	require.Empty(t, stale, "rows no resolution site matches any more: remove them")

	wrapper := concreteMethods(reflect.TypeFor[*booksJobsStore]())
	pebble := concreteMethods(reflect.TypeFor[*database.PebbleStore]())
	_, unwraps := any(&booksJobsStore{}).(database.StoreUnwrapper)
	require.False(t, unwraps)

	for _, k := range scanSortedKeys(jobCapabilityRows) {
		row, si := jobCapabilityRows[k], found[k]
		t.Run(k, func(t *testing.T) {
			require.NotEmpty(t, row.why)
			switch row.class {
			case capImplemented:
				want := capIfaceMethods(t, si.typ)
				require.NotEmpty(t, want)
				require.Empty(t, missingFrom(want, wrapper),
					"%s: the wrapper does not satisfy it, so the code that resolves it from the wrapper finds a store without it", k)
			case capNoStore:
				want := capIfaceMethods(t, si.typ)
				require.NotEmpty(t, missingFrom(want, pebble),
					"%s: the real store now implements it: classify it as a read or a write", k)
				require.NotEmpty(t, missingFrom(want, wrapper), "%s: the wrapper offers it", k)
			case capConcrete:
				_, isIface := si.typ.Underlying().(*types.Interface)
				require.False(t, isIface, "%s is an interface", k)
				require.NotEqual(t, strings.TrimPrefix(types.TypeString(si.typ, qual), "*"), "booksJobsStore")
			default:
				t.Fatalf("%s: unknown class %q", k, row.class)
			}
		})
	}
}

// TestJobCapabilityScanSeesTheCapabilitiesItWasBuiltFor pins the scan itself
// against a silent empty result (a load that found no job, a walk that stopped
// at the first call): the jobs and capabilities named here are the ones whose
// absence would mean the scan broke, not that the code changed.
func TestJobCapabilityScanSeesTheCapabilitiesItWasBuiltFor(t *testing.T) {
	sites := jobSites(t)
	require.Contains(t, sites, "package merge")
	for _, id := range []string{"merge-chapter-groups", "cleanup-series", "scan-composer-tags", "sweep-pebble-metrics-ttl", "refetch-missing-authors"} {
		require.Contains(t, sites, id, "the scan found no job %q", id)
	}
	require.Contains(t, sites["merge-chapter-groups"], "assert dedup.Store")
	require.Contains(t, sites["cleanup-series"], "AsCapability[store.SeriesUnreferencedDeleter]")
	require.Contains(t, sites["sweep-pebble-metrics-ttl"], "AsCapability[*store.PebbleStore]")
	require.Contains(t, sites["package merge"], "AsCapability[merge.rawDeleter]")
	// A store-package helper is entered, so what it resolves is recorded under the
	// capability's own type: BookFileByPathIncludingRetired is
	// AsCapability[RetiredBookFileReader], reached by fix-version-groups.
	require.Contains(t, sites["fix-version-groups"], "AsCapability[store.RetiredBookFileReader]")
	// A call through errors.As by hand is not a store capability.
	for _, ss := range sites {
		for k := range ss {
			require.NotContains(t, k, "{As(any) bool}")
			require.NotContains(t, k, "Unwrap()")
		}
	}
}
