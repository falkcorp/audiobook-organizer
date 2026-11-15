// file: internal/plugins/maintenance/kinds_t6_g5_storediff_test.go
// version: 1.1.1
// guid: 8e2f6a1c-47b3-4d09-9c5e-b13a7d60f4e2
// last-edited: 2026-11-15

package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The method table (t6g5WriterResources) is a CLAIM about what each writer
// method writes, and so are each kind's Effects and the expected write sets.
// A test that only maps recorded method names through that table can be made to
// agree with itself: edit the table row, the Effects and the expected set
// together and every comparison still passes (that is how the clone's missing
// `operations` and `files` got past two rounds). These tests take their
// evidence from the STORE instead: they snapshot every key before and after a
// native run, name the registered key family each changed key belongs to
// (database.KeyFamilies), and map the family to a pkg/ops resource through the
// small table below. A run may only write resources that the kind declares AND
// that the methods the spy saw are stamped with, and a family nobody mapped
// fails the test, so a new store write cannot go unseen.

// t6g5KeyFamilyResource maps a key-family prefix to the pkg/ops resource that
// owns it, by the longest matching prefix. It follows the vocabulary in
// t6g5WriterResources: a derived index belongs to the row it indexes; narrator,
// book-narrator and chapter rows are `books`; preferences, playlists and the
// sync-id rows are `user_state` until pkg/ops names sync identity; the undo
// journal (opchange, the merge sibling-move journal) is `operations`; a record
// keyed on a book or a version group (path_history, group redirects) is `books`.
var t6g5KeyFamilyResource = map[string]ops.Resource{
	"book:":                 ops.ResBooks,
	"book_sig:":             ops.ResBooks,
	"book_atpath":           ops.ResBooks,
	"book_hash:":            ops.ResBooks,
	"book_organizedhash:":   ops.ResBooks,
	"book_originalhash:":    ops.ResBooks,
	"book_narrators:":       ops.ResBooks,
	"book_ver:":             ops.ResBooks,
	"book_tag:":             ops.ResBooks,
	"tag_idx:":              ops.ResBooks,
	"alt_titles:":           ops.ResBooks,
	"ident:":                ops.ResBooks,
	"path_history:":         ops.ResBooks,
	"tombstone:":            ops.ResBooks,
	"chapters:":             ops.ResBooks,
	"narrator":              ops.ResBooks,
	"work:":                 ops.ResBooks,
	"metadata_rejection:":   ops.ResBooks,
	"merge:group-redirect:": ops.ResBooks,
	"b:duration_map:":       ops.ResBooks,
	"book_file":             ops.ResBookFiles,
	"bf:":                   ops.ResBookFiles,
	"bfs:":                  ops.ResBookFiles,
	"file_prov":             ops.ResBookFiles,
	"author:":               ops.ResAuthors,
	"author_":               ops.ResAuthors,
	"book_authors:":         ops.ResAuthors,
	"ubs:":                  ops.ResUserState,
	"upos:":                 ops.ResUserState,
	"playp:":                ops.ResUserState,
	"playe:":                ops.ResUserState,
	"pref:":                 ops.ResUserState,
	// The clone kind's own undo record. It is a user_state write like any other
	// preference and every check judges it (declared, stamped, per call, preview), but
	// it is listed in t6g5SelfRecordKeys so it cannot, on its own, satisfy a
	// run's must-write user_state: that has to come from the state the run moves.
	"pref:_system:itunes_clone_record:": ops.ResUserState,
	"upl:":                              ops.ResUserState,
	"playlist":                          ops.ResUserState,
	"bookmark:":                         ops.ResUserState,
	"user_tag:":                         ops.ResUserState,
	"merge_user_state_pending:":         ops.ResUserState,
	"merge:survivor-reconcile:":         ops.ResUserState,
	"sync_item:":                        ops.ResUserState,
	"sync_file:":                        ops.ResUserState,
	"sync_alias_use":                    ops.ResUserState,
	"idx:":                              ops.ResUserState,
	"opchange":                          ops.ResOperations,
	"merge:sibling-journal:":            ops.ResOperations,
	"merge:combine-journal:":            ops.ResOperations,
	// The undo journal's clock ceiling moves only when the journal writes (opchange rows), so it
	// belongs to the journal. op_result: (a per-book result keyed on an op, not an undo row) is
	// deliberately NOT mapped: no kind of this group writes it, so one that did fails closed.
	"system:journal_clock":  ops.ResOperations,
	"act:":                  ops.ResActivity,
	"setting:":              ops.ResSettings,
	"metadata_state:":       ops.ResFieldStates,
	"emb:":                  ops.ResEmbeddings,
	"dedup":                 ops.ResEmbeddings,
	"metadata_cache:":       ops.ResMetadataCache,
	"pcache:":               ops.ResMetadataCache,
	"metadata_fetch_cache:": ops.ResMetadataCache,
	"fpidx":                 ops.ResFingerprintIndex,
	"fpwin":                 ops.ResFingerprintIndex,
	"ext_id:":               ops.ResExternalIDs,
}

// t6g5ExcludedKeys are the key prefixes a run may change without any kind
// having written them on purpose, each with the reason. The list is narrow by
// design: a family nobody named here and nobody mapped above fails closed, so
// activity (act:), settings (setting:) and the rest of system: are judged like
// every other key. The only dynamic exclusion is the run's own activity index
// (act:op:<runID>), which t6g5DiffStore takes as its skip argument.
var t6g5ExcludedKeys = []struct{ prefix, reason string }{
	{"counter:", "id counters the store bumps whenever any row is created"},
	{"stats:", "cached library and playback stats the store invalidates on any book write"},
	{"met:", "process metrics the store samples"},
	{"syslog:", "system-log entries the host's logger appends during any run"},
	{"quick_query_cache:", "result cache the store invalidates on book writes"},
	{"op:", "operation records the task framework keeps for every run"},
	{"opv2:", "operations v2 rows the task framework keeps for every run"},
	{"opv3:", "operations v3 rows the task framework keeps for every run"},
	{"operation:", "v1 operation rows the task framework keeps for every run"},
	{"operationlog:", "v1 operation logs the task framework keeps for every run"},
	{"opsummary:", "v1 operation summaries the task framework keeps for every run"},
	{"system:backfill:", "index backfill markers the store maintains itself, never a kind"},
	{"system:census:", "the db census the store maintains itself, never a kind"},
	{"system:flag:", "one-time index-built flags the store maintains itself, never a kind"},
	{"system:migration:", "migration stamps the store maintains itself, never a kind"},
	{"system:activity_filter_index_", "activity filter-index cursors the store maintains itself, never a kind"},
	{"setting:system:flag:", "SetSetting form of the one-time index-built flags (store-maintained)"},
	{"setting:book_isbn_index_v1_done", "one-time ISBN index-built flag the store maintains itself"},
}

// t6g5SnapshotOf is every key of the store with its value.
func t6g5SnapshotOf(s *database.PebbleStore) (map[string]string, error) {
	kvs, err := s.ScanPrefix("")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[kv.Key] = string(kv.Value)
	}
	return out, nil
}

// t6g5Snapshot is t6g5SnapshotOf failing the test on a scan error.
func t6g5Snapshot(t *testing.T, s *database.PebbleStore) map[string]string {
	t.Helper()
	out, err := t6g5SnapshotOf(s)
	require.NoError(t, err)
	return out
}

// t6g5StoreDiff is what one run changed in the store.
type t6g5StoreDiff struct {
	// resources is the sorted, unique set of pkg/ops resources the changed keys belong to.
	resources []string
	// sample holds one changed key per resource, for a failure message.
	sample map[string]string
	// problems lists changed keys no registered family or no resource mapping covers.
	problems []string
	// changed counts every changed key outside the excluded keys.
	changed int
	// keys and selfKeys count, per resource, the changed keys and how many of
	// them are a kind's record about its own run (t6g5SelfRecordKeys).
	keys, selfKeys map[string]int
}

// t6g5SelfRecordKeys are key prefixes of a record a kind writes about its own
// run (the clone's undo record). They count as writes everywhere except as
// evidence for a must-write resource, see writes.
var t6g5SelfRecordKeys = []string{"pref:_system:itunes_clone_record:"}

// writes reports whether res was written by something other than the kind's
// own run record, which is what a must-write resource has to be.
func (d t6g5StoreDiff) writes(res string) bool { return d.keys[res] > d.selfKeys[res] }

// t6g5FamilyOf is the longest registered key family that prefixes key, or "".
func t6g5FamilyOf(key string) string {
	best := ""
	for _, f := range database.KeyFamilies() {
		if strings.HasPrefix(key, f.Prefix) && len(f.Prefix) > len(best) {
			best = f.Prefix
		}
	}
	return best
}

// t6g5ResourceOf is the resource owning a key, by the longest matching prefix
// of t6g5KeyFamilyResource (matched against the key, so a single bare key such
// as system:journal_clock can be mapped inside a family that is otherwise
// unmapped), and whether any entry covers it.
func t6g5ResourceOf(key string) (ops.Resource, bool) {
	best, res := "", ops.Resource("")
	for p, r := range t6g5KeyFamilyResource {
		if strings.HasPrefix(key, p) && len(p) > len(best) {
			best, res = p, r
		}
	}
	return res, best != ""
}

// t6g5DiffStore is the resources before -> after changed. skip lists extra key
// prefixes to ignore for this run only (the run's own act:op:<runID> index).
func t6g5DiffStore(before, after map[string]string, skip ...string) t6g5StoreDiff {
	d := t6g5StoreDiff{sample: map[string]string{}, keys: map[string]int{}, selfKeys: map[string]int{}}
	seen := map[string]bool{}
	note := func(key string) {
		for _, ex := range t6g5ExcludedKeys {
			if strings.HasPrefix(key, ex.prefix) {
				return
			}
		}
		for _, sk := range skip {
			if strings.HasPrefix(key, sk) {
				return
			}
		}
		d.changed++
		fam := t6g5FamilyOf(key)
		if fam == "" {
			d.problems = append(d.problems, fmt.Sprintf("%q is in no registered key family", key))
			return
		}
		res, ok := t6g5ResourceOf(key)
		if !ok {
			d.problems = append(d.problems, fmt.Sprintf("%q (family %q) has no resource in t6g5KeyFamilyResource", key, fam))
			return
		}
		d.keys[string(res)]++
		for _, sr := range t6g5SelfRecordKeys {
			if strings.HasPrefix(key, sr) {
				d.selfKeys[string(res)]++
			}
		}
		if !seen[string(res)] {
			seen[string(res)] = true
			d.resources = append(d.resources, string(res))
			d.sample[string(res)] = key
		}
	}
	for k, v := range after {
		if old, ok := before[k]; !ok || old != v {
			note(k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			note(k)
		}
	}
	sort.Strings(d.resources)
	sort.Strings(d.problems)
	return d
}

// t6g5RunNativeDiff is t6g5RunNativeOn over a PebbleStore, with the store's key
// diff taken around the run AND around every writer call (the spy snapshots s
// before and after each one, nested calls included, see t6g5Spy.note).
func t6g5RunNativeDiff(t *testing.T, s *database.PebbleStore, p *Plugin, mode tasks.Mode, headID string, scanKey tasks.Key, params string, opts ...t6g5SpyOption) (t6g5NativeRun, t6g5StoreDiff) {
	t.Helper()
	before := t6g5Snapshot(t, s)
	r := t6g5RunNativeWith(t, s, p, mode, headID, scanKey, params, opts...)
	return r, t6g5DiffStore(before, t6g5Snapshot(t, s), "act:op:"+r.runID)
}

// requireStoreWritesWithin fails unless every resource the run wrote to the
// store is declared by the kind, is stamped on at least one method the spy saw,
// and (per method) is within the row of the method whose call changed it; and
// every resource in mustWrite was written. Three sources are compared to the
// claims, the store's keys, the spy's method calls and the keys each call
// changed, so the claims cannot be edited to agree with each other, and a
// method whose row lost a resource is caught by its own call even when a
// sibling method's row still stamps that resource.
func (r t6g5NativeRun) requireStoreWritesWithin(t *testing.T, d t6g5StoreDiff, mustWrite ...string) {
	t.Helper()
	require.Empty(t, d.problems, "every key the run changed belongs to a registered family with a resource mapping")
	require.NotEmpty(t, d.resources, "a live run of a writing kind changed the store, or the subset checks below prove nothing")
	declared := t6g5WriteKinds(t, tasks.CommonOf(r.def).Effects)
	stamped := map[string]bool{}
	r.spy.mu.Lock()
	for _, c := range r.spy.calls {
		for _, res := range t6g5WriterResources[c] {
			stamped[string(res)] = true
		}
	}
	r.spy.mu.Unlock()
	for _, res := range d.resources {
		assert.Contains(t, declared, res, "the run wrote %q (key %q), which %s does not declare in its Effects", res, d.sample[res], r.def.Kind())
		assert.True(t, stamped[res], "the run wrote %q (key %q), which no method the run called is stamped with in t6g5WriterResources", res, d.sample[res])
	}
	for _, res := range mustWrite {
		assert.True(t, d.writes(res), "the run was expected to write %q to the store (other than a record about its own run)", res)
	}
	r.requireEachCallWithinItsRow(t)
}

// requireEachCallWithinItsRow checks the per-call attribution: the keys each
// writer call changed map to resources inside THAT method's row of
// t6g5WriterResources. A nested call (the delete closure CarryStateThenHardDelete
// runs arrives as its own DeleteBook) is attributed to its own method and removed
// from the outer call's diff. The run fails if writer calls from two goroutines
// overlapped, since snapshots cannot tell them apart and the check would be
// vacuous: none of this group's fixtures does that, so a failure means a kind
// started calling the writer from a pool and the check needs a design for it.
func (r t6g5NativeRun) requireEachCallWithinItsRow(t *testing.T) {
	t.Helper()
	r.spy.mu.Lock()
	defer r.spy.mu.Unlock()
	assert.Empty(t, r.spy.attrProblems, "every key a writer call changed belongs to a registered family with a resource mapping")
	require.False(t, r.spy.attrConcurrent, "%s called the writer from two goroutines at once: the per-call attribution cannot attribute those writes", r.def.Kind())
	for method, seen := range r.spy.observed {
		row := map[string]bool{}
		for _, res := range t6g5WriterResources[method] {
			row[string(res)] = true
		}
		for res, key := range seen {
			assert.True(t, row[res], "%s changed %q (key %q), which is not in its row of t6g5WriterResources %v", method, res, key, t6g5WriterResources[method])
		}
	}
}

// requireMethodWrote pins that a call of method changed the store in each of
// the named resources: it proves a fixture exercises the write a row claims,
// so the per-call check above is not vacuous for that row.
func (r t6g5NativeRun) requireMethodWrote(t *testing.T, method string, resources ...string) {
	t.Helper()
	r.spy.mu.Lock()
	defer r.spy.mu.Unlock()
	require.False(t, r.spy.attrConcurrent, "the per-call attribution was off for this run (concurrent writer calls), so %s's writes were not observed", method)
	for _, res := range resources {
		_, ok := r.spy.observed[method][res]
		assert.True(t, ok, "a call of %s was expected to change a %q key; it changed %v", method, res, r.spy.observed[method])
	}
}

// requireNoStoreWrites is the preview contract: no writer method was called and
// no key outside the named exclusions (t6g5ExcludedKeys, the run's own act:op:
// index) changed.
func (r t6g5NativeRun) requireNoStoreWrites(t *testing.T, d t6g5StoreDiff) {
	t.Helper()
	assert.Empty(t, r.spy.calls, "a preview calls no writer method")
	assert.Empty(t, d.problems)
	assert.Zero(t, d.changed, "a preview changes no key in the store (changed resources: %v)", d.resources)
}

// The diff machinery must fail a write the claims do not cover and must pass a
// covered one, or the live checks below would be vacuous.
func TestT6G5StoreDiff_NamesTheResourceOfAChangedKey(t *testing.T) {
	before := map[string]string{"book:01A": "x", "ubs:u:01A": "p"}
	after := map[string]string{"book:01A": "y", "ubs:u:01A": "p", "opchange:01B": "c", "counter:book": "9"}
	d := t6g5DiffStore(before, after)
	assert.Empty(t, d.problems)
	assert.Equal(t, []string{string(ops.ResBooks), string(ops.ResOperations)}, d.resources,
		"an edit and a new key map to their resources; a framework counter and an unchanged key are ignored")
	assert.Equal(t, 2, d.changed)

	gone := t6g5DiffStore(map[string]string{"book_file:id:F1": "r"}, map[string]string{})
	assert.Equal(t, []string{string(ops.ResBookFiles)}, gone.resources, "a deleted key counts")

	odd := t6g5DiffStore(nil, map[string]string{"zzz-no-such-family": "1", "cat:x": "1"})
	assert.Len(t, odd.problems, 2, "an unregistered key and an unmapped family both fail closed")
}

// TestT6G5StoreDiff_EveryFamilyMappingIsAnOpsConstantValue: the values of the
// family table are real pkg/ops constants, and its keys are registered families
// or prefixes of registered families, so a typo cannot mute a mapping.
func TestT6G5StoreDiff_EveryFamilyMappingIsAnOpsConstantValue(t *testing.T) {
	values := opsResourceValues(t)
	for prefix, res := range t6g5KeyFamilyResource {
		assert.True(t, values[string(res)], "%q maps to %q, which is no pkg/ops constant", prefix, res)
		found := false
		for _, f := range database.KeyFamilies() {
			if strings.HasPrefix(f.Prefix, prefix) || strings.HasPrefix(prefix, f.Prefix) {
				found = true
				break
			}
		}
		assert.True(t, found, "%q is inside or around no registered key family", prefix)
	}
	for _, ex := range t6g5ExcludedKeys {
		assert.NotEmpty(t, ex.reason, "excluded prefix %q names its reason", ex.prefix)
		found := false
		for _, f := range database.KeyFamilies() {
			if strings.HasPrefix(f.Prefix, ex.prefix) || strings.HasPrefix(ex.prefix, f.Prefix) {
				found = true
				break
			}
		}
		assert.True(t, found, "excluded prefix %q is inside or around no registered key family", ex.prefix)
	}
}

// The rulings the table depends on, pinned by key: a record keyed on a book
// (path_history) is a books write, the undo journals (opchange, the merge
// sibling-move journal and its clock) are operations, activity and settings
// are their own resources, and a key nobody mapped is a problem, never a pass.
func TestT6G5StoreDiff_RulingsAreMapped(t *testing.T) {
	cases := map[string]ops.Resource{
		"path_history:01BATMAN00000000000000PH01:1": ops.ResBooks,
		"opchange:01BATMAN00000000000000OP01:1":     ops.ResOperations,
		"merge:sibling-journal:01BATMAN0000000001":  ops.ResOperations,
		"system:journal_clock":                      ops.ResOperations,
		"act:info:01BATMAN00000000000000AC01":       ops.ResActivity,
		"setting:repairs_last_plan_op:fixer":        ops.ResSettings,
	}
	for key, want := range cases {
		d := t6g5DiffStore(nil, map[string]string{key: "v"})
		assert.Empty(t, d.problems, key)
		assert.Equal(t, []string{string(want)}, d.resources, "%s is a %s write", key, want)
	}
	d := t6g5DiffStore(nil, map[string]string{"op_result:01BATMAN00000000000000OP01:b": "v", "system:unheard:of": "v"})
	assert.Len(t, d.problems, 2, "op_result: is off-vocabulary for this group and an unmapped system: key fails closed")
	rec := t6g5DiffStore(nil, map[string]string{"pref:_system:itunes_clone_record:vg-t": "v"})
	assert.Empty(t, rec.problems)
	assert.Equal(t, []string{string(ops.ResUserState)}, rec.resources, "the clone's own record is a user_state write everywhere")
	assert.False(t, rec.writes(string(ops.ResUserState)), "but it alone is no evidence of user state a run was expected to move")
	both := t6g5DiffStore(nil, map[string]string{"pref:_system:itunes_clone_record:vg-t": "v", "ubs:alfred:01BATMAN00000000000000UB01": "s"})
	assert.True(t, both.writes(string(ops.ResUserState)), "a real user-state key next to the record is")
	skipped := t6g5DiffStore(nil, map[string]string{"act:op:RUN1:x": "v", "act:op:RUN2:x": "v"}, "act:op:RUN1")
	assert.Equal(t, []string{string(ops.ResActivity)}, skipped.resources, "only the run's own act:op: index is skipped; another run's rows are activity writes")
	assert.Equal(t, 1, skipped.changed)
}

// --- every writing kind's preview writes nothing ---

type t6g5PreviewCase struct {
	headID string
	scan   tasks.Key
	// build returns the store, the plugin over it and the params a live run of
	// the same fixture would take.
	build func(t *testing.T) (*database.PebbleStore, *Plugin, string)
}

func t6g5PreviewCases() []t6g5PreviewCase {
	return []t6g5PreviewCase{
		{"maintenance.repair-library-state", "", func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			s := newRepairPebble(t)
			root := t.TempDir()
			state, hash := libStateImported, "deadbeef"
			_, err := s.CreateBook(&database.Book{ID: "01BATMAN00000000000000LS02", Title: "Arkham Asylum",
				FilePath: filepath.Join(root, "Bruce Wayne", "Arkham Asylum", "01.mp3"), LibraryState: &state, OrganizedFileHash: &hash})
			require.NoError(t, err)
			return s, New(fakeDeps{store: s, root: root}), ""
		}},
		{"maintenance.mark-missing-files", t6g5ScanKey, func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			s := newRepairPebble(t)
			dir := t.TempDir()
			_, err := s.CreateBook(&database.Book{ID: "01BATMAN00000000000000MM02", Title: "Year One", FilePath: dir})
			require.NoError(t, err)
			require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01ROWMM0000000000000000002", BookID: "01BATMAN00000000000000MM02", FilePath: filepath.Join(dir, "gone.mp3")}))
			return s, New(fakeDeps{store: s, root: dir}), `{"reportPath":"` + filepath.Join(dir, "r.tsv") + `"}`
		}},
		{"maintenance.missing-file-repoint", t6g5ScanKey, func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			s := newRepairPebble(t)
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Year One - 02.mp3"), make([]byte, 1234), 0o644))
			const book = "01BATMAN00000000000000RP02"
			_, err := s.CreateBook(&database.Book{ID: book, Title: "The Long Halloween", FilePath: dir})
			require.NoError(t, err)
			require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01ROWRP0000000000000000002", BookID: book, FilePath: filepath.Join(dir, "Year One - 2", "35.mp3"), FileSize: 1234}))
			return s, New(fakeDeps{store: s, root: dir}), `{"reportPath":"` + filepath.Join(dir, "r.tsv") + `"}`
		}},
		{"maintenance.merge-same-path-dupes", "", func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			s := newRepairPebble(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "Bruce Wayne", "Hush.m4b")
			for _, id := range []string{"01BATMAN00000000000000MG03", "01BATMAN00000000000000MG04"} {
				_, err := s.CreateBook(&database.Book{ID: id, Title: "Hush", FilePath: path})
				require.NoError(t, err)
				require.NoError(t, s.CreateBookFile(&database.BookFile{BookID: id, FilePath: path, FileHash: "H"}))
			}
			return s, New(fakeDeps{store: s, root: dir}), `{"reportPath":"` + filepath.Join(dir, "r.tsv") + `"}`
		}},
		{"maintenance.metadata-cache-reap", "", func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			s := newRepairPebble(t)
			dir := t.TempDir()
			require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: "01BATMAN00000000000000OR02", FetchedAt: time.Now()}))
			return s, New(fakeDeps{store: s, root: dir}), `{"reportPath":"` + filepath.Join(dir, "r.tsv") + `"}`
		}},
		{"maintenance.move-book-file-rows", t6g5ScanKey, func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			f := newMBFFixture(t)
			return f.s, New(fakeDeps{store: f.s, root: t.TempDir()}), `{"moves":[{"row_id":"` + f.ch01 + `","to_book_id":"` + f.dst + `"}]}`
		}},
		{"maintenance.purge-empty-narrators", t6g5ScanKey, func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			s := newSeriesPhantomStore(t)
			_, err := s.CreateNarrator("Selina Kyle")
			require.NoError(t, err)
			return s, New(fakeDeps{store: s, root: t.TempDir()}), ""
		}},
		{"maintenance.split-joined-narrators", t6g5ScanKey, func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			f := newSplitJoinedFixture(t)
			return f.s, New(fakeDeps{store: f.s, root: t.TempDir()}), ""
		}},
		{"itunes.clone-into-library", t6g5ScanKey, func(t *testing.T) (*database.PebbleStore, *Plugin, string) {
			f := newICFixture(t)
			f.book(t, "T", "vg-t", "Year One", []string{f.itunes("Year One.m4b")})
			// apply:true and a real group: the preview must still write nothing.
			return f.s, t6g5ClonePlugin(f), `{"apply":true,"group_ids":["vg-t"]}`
		}},
	}
}

// Every writing kind of this group, run in preview over the same fixture its
// live test uses, calls no writer method and changes no key. A kind that wrote
// in preview (a dry-run flag read the wrong way round, a gate dropped) fails
// here, and does not wait for the engine's ErrPreview to catch it after F5-18.
func TestT6G5Native_EveryWritingKindPreviewWritesNothing(t *testing.T) {
	cases := t6g5PreviewCases()
	covered := map[string]bool{"itunes.playlist-import": true, "itunes.regroup": true} // their own preview tests
	for _, c := range cases {
		covered[c.headID] = true
	}
	for _, w := range t6g5Wants() {
		if !w.readOnly {
			assert.True(t, covered[w.headID], "%s is a writing kind with a preview-writes-nothing test", w.headID)
		}
	}
	for _, c := range cases {
		t.Run(c.headID, func(t *testing.T) {
			s, p, params := c.build(t)
			r, d := t6g5RunNativeDiff(t, s, p, tasks.ModePreview, c.headID, c.scan, params)
			require.NoError(t, r.err)
			r.requireNoStoreWrites(t, d)
		})
	}
}
