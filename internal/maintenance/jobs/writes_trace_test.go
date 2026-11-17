// file: internal/maintenance/jobs/writes_trace_test.go
// version: 1.0.0
// guid: c58e1d07-3a92-4f64-b0d3-7e49a2f6c815
// last-edited: 2026-11-16

package jobs_test

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/mediacore/tasks"
)

// What these tests guard. jobWrites used to be a second hand-written list beside
// the shared method-to-resource table (internal/maintenance/resources). It said
// enrich-book-files writes only book_files while the table says UpdateBookFile
// writes book_files and the whole book row, and no job that calls ModifyBook
// declared metadata_cache or indexes, which the store writes underneath that call
// (stack review M2). The old oracle classified a call by its name and stopped at
// the store, so it could not see either.
//
// Here the answer comes from two independent sources: the SSA oracle finds the
// store methods each job's Run reaches, and the shared table says what each
// method writes. kinds_writes.go (jobTraces) is a third statement of the same
// facts and these tests hold it to the other two.

// traceVerb is oracleVerb plus the verbs the store package uses for helpers that
// read as writes; a call whose name matches it and is neither in the shared table
// nor in traceInert fails the trace test, so a new write cannot go unclassified.
var traceVerb = regexp.MustCompile(oracleVerb.String() + `|^(Import|Repoint|Drop|Lock|Heal|Carry)`)

// traceInert are the calls the oracle sees whose names read as writes and that
// change nothing durable: each edits a value in memory, takes an in-process lock
// or reads. The row of the file or book they feed is written by the store method
// that follows them, which is in the trace.
var traceInert = map[string]string{
	"SetMeasuredDuration":        "sets a field on the in-memory BookFile; the row is written by the file-row method that follows",
	"RecordDurationProbeFailure": "sets fields on the in-memory BookFile; the row is written by the file-row method that follows",
	"LockUserBookState":          "takes an in-process mutex",
	"LockUserBookStates":         "takes in-process mutexes",
	"LockUserBookStatePair":      "takes in-process mutexes",
	"Move":                       "SeriesBooksMap.Move edits a map in memory",
	"DropDanglingSeriesRef":      "clears SeriesID on the in-memory book; the ModifyBook that follows writes it",
	"ApplyRespectingLocks":       "runs the mutate closure on the in-memory book and restores the locked fields",
	"RetiredClaimOnPath":         "reads book_file rows",
	"Locked":                     "FieldLocks.Locked reads a lock flag",
	"StoredAggregateSec":         "BookRuntime.StoredAggregateSec reads a stored value",
}

// traceAliases name a call by its full "<receiver>.<method>" key when its method
// name is not the store method it stands for.
var traceAliases = map[string]string{
	// The checkpoint interface of backfill-file-hashes and recompute-book-aggregates
	// stores its blob through SaveOperationState (checkpointerFrom).
	"jobs.checkpointer.Save": "SaveOperationState",
	// tagger.LibraryImporter has one implementation, deluge.LibraryImporterAdapter,
	// whose ImportPath ends in ImportToLibraryWith and so in Store.UpdateBookFile
	// (internal/deluge/importer_adapter.go:94), the call bulk-deluge-import makes.
	// The oracle does not follow an interface call into its implementation.
	"tagger.LibraryImporter.ImportPath": "UpdateBookFile",
}

// traceWriteName is the store method a call key stands for, and whether it is a
// write the trace has to account for.
func traceWriteName(table map[string][]string, key string) (string, bool) {
	if a, ok := traceAliases[key]; ok {
		return a, true
	}
	i := strings.LastIndex(key, ".")
	recv, name := key[:i], key[i+1:]
	if oracleNotAWrite.MatchString(recv) {
		return "", false
	}
	if _, ok := traceInert[name]; ok {
		return "", false
	}
	if _, ok := table[name]; ok || traceVerb.MatchString(name) {
		return name, true
	}
	return "", false
}

// sharedTable is resources.Table with its rows as sorted strings.
func sharedTable() map[string][]string {
	out := map[string][]string{}
	for m, row := range resources.Table() {
		rs := make([]string, len(row))
		for i, r := range row {
			rs[i] = string(r)
		}
		sort.Strings(rs)
		out[m] = rs
	}
	return out
}

// outsideTableAndInTable are the Outside entries whose method name also has a
// row in the table, because the name means a different key family here. Every
// other Outside method has no row (TestJobWrites_OutsideTableMethodsHaveNoRow).
var outsideTableAndInTable = map[string]string{
	"retention-and-hygiene DeleteRaw": "deletes the dead book:series: and book:author: key prefixes; the table's DeleteRaw row is the user_state keys",
}

// TestJobWrites_TraceMatchesCode: the store methods jobTraces lists per job are
// exactly the write methods the job's Run reaches. A method the code reaches that
// the trace lacks fails (an under-declared job); so does a method the trace lists
// that the code no longer reaches (a stale row that keeps excluding for nothing).
func TestJobWrites_TraceMatchesCode(t *testing.T) {
	derived := oracleJobs(t)
	table := sharedTable()
	traces := jobs.JobTraces()
	if len(traces) != len(derived) {
		t.Errorf("%d traces but the oracle found %d jobs", len(traces), len(derived))
	}
	for id, j := range derived {
		tr, ok := traces[id]
		if !ok {
			t.Errorf("%s: no trace in jobTraces", id)
			continue
		}
		code := map[string]bool{}
		for key := range j.calls {
			if name, ok := traceWriteName(table, key); ok {
				code[name] = true
			}
		}
		want := map[string]bool{}
		for _, m := range tr.Methods {
			want[m] = true
		}
		disk := false
		for _, o := range tr.Outside {
			if o.Via == jobs.ViaDisk {
				disk = true
				continue
			}
			want[o.Via] = true
		}
		for name := range code {
			if !want[name] {
				t.Errorf("%s: the code reaches write method %s (%v), which jobTraces does not list", id, name, evidenceFor(j, name))
			}
		}
		for name := range want {
			if !code[name] {
				t.Errorf("%s: jobTraces lists %s, which the job's code does not reach", id, name)
			}
		}
		if codeDisk := slices.Contains(j.writes, "files"); codeDisk != disk {
			t.Errorf("%s: the code writes files on disk = %v, jobTraces declares %v", id, codeDisk, disk)
		}
	}
}

// evidenceFor names where the oracle saw a write method, for a failure message.
func evidenceFor(j oracleJob, name string) []string {
	var out []string
	for key, in := range j.calls {
		if strings.HasSuffix(key, "."+name) {
			out = append(out, key+" in "+in)
		}
	}
	sort.Strings(out)
	return out
}

// TestJobWrites_OutsideTableMethodsHaveNoRow: an Outside entry is a store method
// the shared table does not list. The day the table gets its row the method has to
// move to Methods, so the table decides what it writes and the job's set cannot
// keep a stale guess. The only exceptions name a different key family.
func TestJobWrites_OutsideTableMethodsHaveNoRow(t *testing.T) {
	table := sharedTable()
	seen := map[string]bool{}
	for id, tr := range jobs.JobTraces() {
		for _, o := range tr.Outside {
			if o.Via == jobs.ViaDisk {
				continue
			}
			_, hasRow := table[o.Via]
			_, excused := outsideTableAndInTable[id+" "+o.Via]
			if excused {
				seen[id+" "+o.Via] = true
			}
			if hasRow && !excused {
				t.Errorf("%s: %s is listed Outside but the shared table has a row for it (%v); move it to Methods", id, o.Via, table[o.Via])
			}
			if !hasRow && excused {
				t.Errorf("%s: %s is excused as a different key family but the table has no row of that name", id, o.Via)
			}
		}
	}
	for k := range outsideTableAndInTable {
		if !seen[k] {
			t.Errorf("outsideTableAndInTable has %q, which is no Outside entry", k)
		}
	}
}

// TestJobWrites_DerivedFromCodeAndSharedTable is the drift test: each job's
// declared write set is recomputed from the write methods its code reaches (the
// SSA oracle) and the shared table, and must be identical. Outside resources are
// the only input that is not the table's, and only for the store methods the
// table does not list. A row widened or narrowed in the table, a job that starts
// calling another method, or a kind that declares anything else fails by job name.
func TestJobWrites_DerivedFromCodeAndSharedTable(t *testing.T) {
	derived := oracleJobs(t)
	table := sharedTable()
	traces := jobs.JobTraces()
	for _, d := range jobs.Kinds(jobs.KindDeps{}, nil) {
		id := strings.TrimPrefix(string(d.Kind()), "books.maintenance.")
		j, ok := derived[id]
		if !ok {
			t.Errorf("%s: the oracle found no job of that ID", id)
			continue
		}
		outside := map[string]string{}
		for _, o := range traces[id].Outside {
			if o.Via != jobs.ViaDisk {
				outside[o.Via] = o.Res
			}
		}
		set := map[string]bool{}
		for key := range j.calls {
			name, ok := traceWriteName(table, key)
			if !ok {
				continue
			}
			if res, isOutside := outside[name]; isOutside {
				set[res] = true
				continue
			}
			row, ok := table[name]
			if !ok {
				t.Errorf("%s: write method %s is in neither the shared table nor jobTraces' Outside (%v)", id, name, evidenceFor(j, name))
				continue
			}
			for _, r := range row {
				set[r] = true
			}
		}
		if slices.Contains(j.writes, "files") {
			set["files"] = true
		}
		var want []string
		for r := range set {
			want = append(want, r)
		}
		sort.Strings(want)
		var got []string
		for _, r := range tasks.CommonOf(d).Effects.WriteSet() {
			got = append(got, r.Kind)
		}
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: declares %v, but its code and the shared table give %v", id, got, want)
		}
	}
}

// jobWritesPinned is every job's declared write set typed by hand: space
// separated pkg/ops resource names. The other tests derive the expectation from
// the table, so a resource dropped from a row shrinks the declaration and the
// expectation together and nothing fails (the header of
// internal/plugins/maintenance/kinds_effects_pinned_test.go has the F5-11 case).
// This literal does not move with the table. Changing a job's set on purpose, or
// a row it reaches, means editing the word here in the same change.
var jobWritesPinned = map[string]string{
	"backfill-book-files":             "authors book_files books indexes metadata_cache operations",
	"backfill-file-hashes":            "authors book_files books files indexes metadata_cache operations",
	"backfill-itunes-positions":       "user_state",
	"backfill-merge-undo-pointers":    "operations",
	"backfill-metadata-source-hash":   "authors books indexes metadata_cache",
	"backfill-sync-ids":               "sync_identity",
	"bulk-deluge-import":              "authors book_files books files indexes metadata_cache operations",
	"cleanup-empty-folders":           "files",
	"cleanup-organize-mess":           "files",
	"cleanup-series":                  "authors books indexes metadata_cache series",
	"dedup-books":                     "authors book_files books external_ids indexes metadata_cache user_state",
	"enrich-book-files":               "authors book_files books indexes metadata_cache",
	"fix-author-narrator-swap":        "authors books indexes metadata_cache",
	"fix-file-modes":                  "files",
	"fix-read-by-narrator":            "authors books indexes metadata_cache",
	"fix-version-groups":              "authors book_files books indexes metadata_cache",
	"merge-chapter-groups":            "authors book_files books external_ids indexes metadata_cache operations sync_identity user_state",
	"normalize-primary-flags":         "authors books indexes metadata_cache",
	"prune-book-snapshots":            "books",
	"purge-unknown-author-duplicates": "authors books indexes metadata_cache",
	"recompute-book-aggregates":       "authors books indexes metadata_cache operations",
	"recompute-itunes-paths":          "authors book_files books indexes metadata_cache",
	"refetch-missing-authors":         "authors books indexes metadata_cache",
	"relink-missing-to-itunes":        "authors book_files books indexes metadata_cache",
	"relink-report":                   "",
	"repoint-version-primary":         "authors books indexes metadata_cache",
	"retention-and-hygiene":           "operations settings",
	"revert-metadata-fetch":           "authors books indexes metadata_cache",
	"scan-chapter-groups":             "",
	"scan-composer-tags":              "authors book_files books file_provenance files indexes metadata_cache operations",
	"scan-duplicate-files":            "",
	"scan-duration-mismatch":          "",
	"scan-metadata-hash-dups":         "",
	"sweep-pebble-metrics-ttl":        "metrics",
}

// declaredWrites is the sorted resource kinds a kind declares it writes.
func declaredWrites(d tasks.Definition) []string {
	var got []string
	for _, r := range tasks.CommonOf(d).Effects.WriteSet() {
		got = append(got, r.Kind)
	}
	sort.Strings(got)
	return got
}

// TestJobWrites_PinnedLiteral compares every kind's declared write set with the
// hand-typed jobWritesPinned.
func TestJobWrites_PinnedLiteral(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range jobs.Kinds(jobs.KindDeps{}, nil) {
		id := strings.TrimPrefix(string(d.Kind()), "books.maintenance.")
		seen[id] = true
		pin, ok := jobWritesPinned[id]
		if !ok {
			t.Errorf("%s: no entry in jobWritesPinned", id)
			continue
		}
		want := strings.Fields(pin)
		sort.Strings(want)
		if got := declaredWrites(d); !slices.Equal(got, want) {
			t.Errorf("%s: declares %v, pinned %v", id, got, want)
		}
	}
	for id := range jobWritesPinned {
		if !seen[id] {
			t.Errorf("jobWritesPinned has %q, which is no registered job", id)
		}
	}
}

// TestJobWrites_NamedBookWriters: enrich-book-files, recompute-itunes-paths and
// backfill-book-files write book rows (UpdateBookFile and BatchCreateBookFiles
// recompute the books aggregate through ModifyBook) and used to declare only
// book_files. They run beside kinds that declare only books while they rewrite
// book rows, so `books` has to be in their set.
func TestJobWrites_NamedBookWriters(t *testing.T) {
	named := map[string]bool{"enrich-book-files": false, "recompute-itunes-paths": false, "backfill-book-files": false}
	for _, d := range jobs.Kinds(jobs.KindDeps{}, nil) {
		id := strings.TrimPrefix(string(d.Kind()), "books.maintenance.")
		if _, ok := named[id]; !ok {
			continue
		}
		named[id] = true
		got := declaredWrites(d)
		for _, want := range []string{"books", "book_files"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s declares %v, which lacks %s", id, got, want)
			}
		}
	}
	for id, found := range named {
		if !found {
			t.Errorf("%s is no registered job", id)
		}
	}
}

// bookWriteFamily are the store methods that reach ModifyBook (or the aggregate
// recompute that calls it), by name, typed by hand: ModifyBook itself, the
// aggregate recompute, and the file-row methods that notify it. Whatever the
// caller's closure sets, the store writes the stale stamp on metadata_cache and
// the ident: rows (indexes) underneath these calls.
var bookWriteFamily = []string{
	"ModifyBook", "UpdateBook", "RecomputeBookAggregates", "SetLibraryStateOrganized",
	"CreateBookFile", "UpdateBookFile", "UpdateBookFiles", "ModifyBookFile",
	"ModifyBookFileReportingRecompute", "SetBookFileHash", "MoveBookFilesToBook",
	"MoveBookFilesToBookBulk", "DeleteBookFilesByIDs", "BatchCreateBookFiles",
}

// TestJobWrites_BookWriteFamilyDeclaresCacheAndIndexes: every job whose code calls
// a ModifyBook-family method declares books, authors, metadata_cache and indexes.
// The callers come from the SSA oracle, not from a list, so a job that starts
// calling ModifyBook is held to it.
func TestJobWrites_BookWriteFamilyDeclaresCacheAndIndexes(t *testing.T) {
	derived := oracleJobs(t)
	table := sharedTable()
	callers := 0
	for _, d := range jobs.Kinds(jobs.KindDeps{}, nil) {
		id := strings.TrimPrefix(string(d.Kind()), "books.maintenance.")
		reaches := ""
		for key := range derived[id].calls {
			name, ok := traceWriteName(table, key)
			if ok && slices.Contains(bookWriteFamily, name) {
				reaches = name
				break
			}
		}
		if reaches == "" {
			continue
		}
		callers++
		got := declaredWrites(d)
		for _, want := range []string{"authors", "books", "indexes", "metadata_cache"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s calls %s but declares %v, which lacks %s", id, reaches, got, want)
			}
		}
	}
	// 19 of the 34 jobs reach the family today; a drop means the oracle stopped
	// seeing calls, not that the jobs stopped writing books.
	if callers < 15 {
		t.Errorf("only %d jobs reach a ModifyBook-family method; the oracle has lost sight of the calls", callers)
	}
}
