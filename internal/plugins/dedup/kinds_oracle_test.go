// file: internal/plugins/dedup/kinds_oracle_test.go
// version: 1.0.0
// guid: 1000e5e8-d012-4005-8c6e-bbe124464ed6
// last-edited: 2026-11-19

package dedup

import (
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The effects oracle (F5-04 review m5). A kind's declared write set used to be
// checked only against a literal the builder wrote beside it, so an under-declared
// write passed. Now each probe's OBSERVED writes, the calls that reached the
// recording writer or engine in a Live run, are mapped to the resources the real
// implementation writes, and every one must be inside the kind's declared Effects.
//
// Three tables classify every first word a spy records; a word in none of them
// fails the test, so a write nobody classified cannot slip by:
//
//   - oracleWriters: a writer-surface method (one of the *Writer interfaces this
//     package owns). Its resources come from internal/maintenance/resources.Table
//     where the store method has a row (ModifyBook, SetSetting, MergeBooks), and
//     otherwise from the key family the real store method writes, cited per entry.
//   - oracleEngine: an engine method (Engine.FullScan and the rest). The engine
//     writes through its own stores where tasklint cannot see it, so these stay a
//     documented gap (seams.d/F5-04.md, "Engine writes are the engine's"); the
//     declared sets for engine-backed kinds are hand-derived and cited in a
//     comment on each kind.
//   - oracleReads: a recorded call that writes nothing.

// tableRow is Table's row for a store method, failing the test if there is none.
func tableRow(t *testing.T, method string) []ops.Resource {
	t.Helper()
	rows, ok := resources.Table()[method]
	require.True(t, ok, "resources.Table has no row for %s", method)
	return rows
}

// oracleWriters maps a recorded writer-surface call word to what it writes.
func oracleWriters(t *testing.T) map[string][]ops.Resource {
	t.Helper()
	settings := tableRow(t, "SetSetting")
	return map[string][]ops.Resource{
		// Main-keyspace store methods: the maintenance writer table's own rows.
		"ModifyBook": tableRow(t, "ModifyBook"),
		"SetSetting": settings,
		// A split-book cluster merge: ModifyBook on the keep, the sources'
		// soft-delete, the sources' files moved to the keep (book_files), the
		// carried external ids, listening state and sync ids, and the undo
		// journal: Table "MergeBooks" plus the file move.
		"MergeSplitBookClusterRechecked": resources.Union(tableRow(t, "MergeBooks"), []ops.Resource{ops.ResBookFiles}),
		// split_book_candidate: rows (SplitBookCandidateStore Save/Delete): the
		// resource this PR added for them.
		"SaveAll":         {ops.ResSplitBookCandidates},
		"DeleteCandidate": {ops.ResSplitBookCandidates},
		// Candidate, label and embedding rows share the embedding keyspace
		// (seams.d/F5-04.md): EmbeddingStore.Delete (emb:v:book, emb:v:author),
		// the labeled-example and candidate writers, the candidate-status index
		// rows (dedup:s:), the f16 re-encode and the flags kept beside them.
		"Delete":                       {ops.ResEmbeddings},
		"Upsert":                       {ops.ResEmbeddings},
		"UpsertLabeledExample":         {ops.ResEmbeddings},
		"ReclassifyCandidate":          {ops.ResEmbeddings},
		"Reclassify":                   {ops.ResEmbeddings},
		"UpdateCandidateScores":        {ops.ResEmbeddings},
		"SyncCandidateWrites":          {ops.ResEmbeddings},
		"WriteStatus":                  {ops.ResEmbeddings},
		"SetCandidateStatusIndexBuilt": {ops.ResEmbeddings},
		"ReencodeRows":                 {ops.ResEmbeddings},
		// The ISBN/ASIN secondary index rows live with the book rows, the flag in
		// settings.
		"WriteISBN":         {ops.ResBooks},
		"SetISBNIndexBuilt": settings,
		// The window-print and LSH indexes: fpwinidx:, fpidx: and fpidx_meta: rows,
		// and their built flags in settings.
		"PutWindow":           {ops.ResFingerprintIndex},
		"SetWindowIndexBuilt": settings,
		"PutLSHEntries":       {ops.ResFingerprintIndex},
		"SetLSHIndexBuilt":    settings,
		// The segment-field drop rewrites BookFile values.
		"Sweep": {ops.ResBookFiles},
		// Persisting a calibrated band config writes the config settings.
		"PersistBandThresholds": settings,
	}
}

// oracleEngine are the engine methods the spies record (the documented gap).
var oracleEngine = map[string]bool{
	"PurgeStaleCandidates": true, "AutoResolveCertain": true, "FullScan": true,
	"Rescore": true, "BookSignatureScan": true, "DrainStaleCandidates": true,
	"EmbedBook": true, "EmbedBooks": true, "EmbedBooksAsync": true, "RunLLMReview": true,
}

// oracleReads are recorded calls that write nothing.
var oracleReads = map[string]bool{
	"GetSetting": true, "ListCandidates": true, "PersistedScoreConfig": true, "ReloadScoreConfig": true,
}

// observedWrites is the resources a probe's calls wrote, by the oracle, and the
// writer-surface call words it saw. It fails on a call word nobody classified.
func observedWrites(t *testing.T, pr writeProbe, writers map[string][]ops.Resource) (got []ops.Resource, words []string) {
	t.Helper()
	seen := map[ops.Resource]bool{}
	for _, call := range pr.Calls() {
		word, _, _ := strings.Cut(call, " ")
		switch rs, isWriter := writers[word]; {
		case isWriter:
			words = append(words, word)
			for _, r := range rs {
				if !seen[r] {
					seen[r] = true
					got = append(got, r)
				}
			}
		case oracleEngine[word], oracleReads[word]:
		default:
			require.Failf(t, "unclassified call", "%s recorded %q: add it to oracleWriters, oracleEngine or oracleReads (kinds_oracle_test.go)", pr.Kind, word)
		}
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	return got, words
}

// requireDeclaredCoversObserved: every resource an observed write touched is in
// the kind's declared write set.
func requireDeclaredCoversObserved(t *testing.T, def tasks.Definition, kind tasks.Kind, observed []ops.Resource) {
	t.Helper()
	declared := map[string]bool{}
	for _, s := range refStrings2(tasks.CommonOf(def).Effects.WriteSet()) {
		declared[s] = true
	}
	for _, r := range observed {
		require.True(t, declared["books/"+string(r)], "%s: an observed write touches %s, which its Effects do not declare (declared %v)",
			kind, r, refStrings2(tasks.CommonOf(def).Effects.WriteSet()))
	}
}
