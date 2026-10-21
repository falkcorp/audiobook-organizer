// file: internal/decisions/retention_test.go
// version: 1.1.0
// guid: 2c8e5b17-94d0-4f63-a1b2-7e03d6c58a49
// last-edited: 2026-10-21

package decisions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Decisions are durable: owner verdicts and labelled training data, never
// pruned. The undo journal (opchange:) is protected from log retention by
// TestPruneOldLogs_LeavesTheUndoJournalUntouched; this file does the same for
// decision: with three layers:
//
//  1. every prune / sweep entry point runs against a store that holds old
//     prunable rows AND decisions, and the decisions must come out byte for
//     byte identical (TestRetention_EveryPruneEntryPointLeavesDecisionsIntact);
//  2. a reflection canary fails when a NEW exported Prune*/Sweep*/Purge*
//     method appears on a store type without being added to that table;
//  3. a source guard fails when anything outside this package and the key
//     registry mentions the decision: prefix (TestRetention_NoOneElseWritesDecisionKeys).
//
// Scope of the canary: it sees methods of the four store types below whose
// names start with Prune, Sweep, Purge, DeleteExpired or Cleanup. Sweeps that
// live on other types (resultjournal.Journal.Prune,
// audiobooks.AudiobookService.PurgeSoftDeletedBooks,
// dedup.Engine.PurgeStaleCandidates) are in retentionEntries BY HAND, because
// reflection over every type in the module is not practical: a new one is
// found by review, and by the source guard, which fails for any non-test file
// outside this package that names the decision: prefix. Plugin-level sweeps
// that delete under their own fixed prefixes
// (internal/plugins/metafetch/pcache_sweep.go, authoritybuild, the iTunes
// write-back cleanup) carry a decision: sentinel in their own survival tests
// (pcache_sweep_test.go and resultjournal's journal_test.go seed one beside
// their other-kind sentinels). A new sweep with a broad or computed range needs
// its own sentinel.

func newKV(t testing.TB) *database.PebbleStore {
	t.Helper()
	p, err := database.NewPebbleStoreInMemory("db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// seedDecisions records a spread that touches every decision key family
// (including membership rows, which need a set over MaxEdgeSet) and returns
// every decision: key with its value.
func seedDecisions(t testing.TB, kv *database.PebbleStore) map[string]string {
	t.Helper()
	s := decisions.New(kv)
	in := func(d decisions.Decision, family string, ids ...string) decisions.Input {
		return decisions.Input{
			OpKind: "repairs:dedup.split-books", EntityType: "book", EntityIDs: ids,
			Decision: d, Source: decisions.SourceHuman, Family: family,
			ActorUserID: "u1", ActorName: "Owner",
		}
	}
	rej := func(family string, ids ...string) decisions.Input {
		i := in(decisions.DecisionReject, family, ids...)
		i.ReasonCode = "distinct_titles"
		return i
	}
	big := make([]string, decisions.MaxEdgeSet+5)
	for i := range big {
		big[i] = fmt.Sprintf("big%03d", i)
	}
	for _, i := range []decisions.Input{
		rej(decisions.FamilySameWork, "b1", "b2", "b3"),
		rej(decisions.FamilySameWork, big...),
		rej("", "solo"),
		in(decisions.DecisionApplied, "", "b8", "b9"),
		in(decisions.DecisionUndone, "", "b8", "b9"),
		in(decisions.DecisionSkip, "", "b7"),
	} {
		_, err := s.Record(i)
		require.NoError(t, err)
	}
	return snapshotDecisions(t, kv)
}

func snapshotDecisions(t testing.TB, kv *database.PebbleStore) map[string]string {
	t.Helper()
	pairs, err := kv.ScanPrefix("decision:")
	require.NoError(t, err)
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		out[p.Key] = string(p.Value)
	}
	return out
}

func requireIntact(t testing.TB, kv *database.PebbleStore, before map[string]string, what string) {
	t.Helper()
	after := snapshotDecisions(t, kv)
	for k, v := range before {
		got, ok := after[k]
		require.True(t, ok, "%s deleted decision key %q", what, k)
		require.Equal(t, v, got, "%s changed decision key %q", what, k)
	}
	require.Len(t, after, len(before), "%s added or removed decision keys", what)
}

func TestRetention_SeedCoversEveryDecisionFamily(t *testing.T) {
	kv := newKV(t)
	before := seedDecisions(t, kv)
	for _, prefix := range decisions.Prefixes() {
		n := 0
		for k := range before {
			if strings.HasPrefix(k, prefix) {
				n++
			}
		}
		require.Positive(t, n, "the retention seed must put at least one row in %s or the survival test proves nothing for it", prefix)
	}
}

// bookIDForSnapshots passes the seeded book from the PruneBookSnapshots seed
// to its run (each entry gets a fresh store; the tests do not run in parallel).
var bookIDForSnapshots string

var (
	oldTime   = time.Now().Add(-400 * 24 * time.Hour)
	pruneCut  = time.Now().Add(-90 * 24 * time.Hour)
	allTiers  = []string{"audit", "batch", "change", "debug", "digest", "info", "system"}
	rawLogRow = func(kind string, i int) (key, val string) {
		switch kind {
		case "operationlog":
			return fmt.Sprintf("operationlog:op%03d:%d:old%d", i%13, oldTime.UnixNano(), i),
				fmt.Sprintf(`{"id":%d,"operation_id":"op","level":"info","message":"m","created_at":%q}`, i, oldTime.Format(time.RFC3339Nano))
		case "syslog":
			ts := oldTime.Add(time.Duration(i) * time.Nanosecond)
			return fmt.Sprintf("syslog:%s:old%d", ts.Format(time.RFC3339Nano), i),
				fmt.Sprintf(`{"source":"s","level":"info","message":"m","created_at":%q}`, ts.Format(time.RFC3339Nano))
		}
		panic(kind)
	}
)

// retentionEntry is one prune or sweep entry point. seed puts prunable rows in
// the store; run invokes the entry point and returns how many rows it deleted.
// wantDeleted is the positive control: the entry point must really have
// deleted something, so "decisions survived" cannot be a vacuous pass.
type retentionEntry struct {
	name        string
	seed        func(t *testing.T, kv *database.PebbleStore)
	run         func(t *testing.T, kv *database.PebbleStore) (int, error)
	wantDeleted bool
}

func seedRawLogs(kind string) func(*testing.T, *database.PebbleStore) {
	return func(t *testing.T, kv *database.PebbleStore) {
		for i := range 20 {
			k, v := rawLogRow(kind, i)
			require.NoError(t, kv.SetRaw(k, []byte(v)))
		}
	}
}

func seedActivity(tier string) func(*testing.T, *database.PebbleStore) {
	return func(t *testing.T, kv *database.PebbleStore) {
		as := database.NewPebbleActivityStore(kv.DB())
		for i := range 5 {
			_, err := as.Record(database.ActivityEntry{
				Timestamp: oldTime.Add(time.Duration(i) * time.Second), Tier: tier, Type: "test",
				Level: "info", Source: "test", Summary: "old row",
			})
			require.NoError(t, err)
		}
	}
}

func retentionEntries() []retentionEntry {
	ctx := context.Background()
	entries := []retentionEntry{
		{
			name: "PebbleStore.PruneOperationLogs", seed: seedRawLogs("operationlog"), wantDeleted: true,
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				return kv.PruneOperationLogs(ctx, pruneCut, nil)
			},
		},
		{
			name: "PebbleStore.PruneSystemActivityLogs", seed: seedRawLogs("syslog"), wantDeleted: true,
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				return kv.PruneSystemActivityLogs(ctx, pruneCut, nil)
			},
		},
		{
			name: "logger.PruneOldLogs", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				seedRawLogs("operationlog")(t, kv)
				seedRawLogs("syslog")(t, kv)
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				return logger.PruneOldLogs(ctx, kv, 90, logger.New("test"), nil)
			},
		},
		{
			name: "PebbleStore.PruneOperationChanges", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				// change_type metadata_update is not in OpChangeTypesKeptByPrune,
				// so these old rows are the ones the prune really deletes.
				for i := range 5 {
					require.NoError(t, kv.SetRaw(fmt.Sprintf("opchange:op-old:c%02d", i),
						[]byte(fmt.Sprintf(`{"id":"c%02d","operation_id":"op-old","book_id":"b","change_type":"metadata_update","created_at":%q}`, i, oldTime.Format(time.RFC3339Nano)))))
				}
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				return kv.PruneOperationChanges(ctx, pruneCut, nil)
			},
		},
		{
			name: "PebbleStore.PruneBookSnapshots", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				book, err := kv.CreateBook(&database.Book{Title: "V1", FilePath: "/test/prune.m4b"})
				require.NoError(t, err)
				for i := 2; i <= 6; i++ {
					book.Title = fmt.Sprintf("V%d", i)
					_, err := kv.UpdateBook(book.ID, book)
					require.NoError(t, err)
					time.Sleep(time.Millisecond)
				}
				bookIDForSnapshots = book.ID
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				return kv.PruneBookSnapshots(bookIDForSnapshots, 2)
			},
		},
		{
			name: "PebbleStore.SweepHollowOperationsV2", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				// An undecodable operation row: the sweep's target.
				require.NoError(t, kv.DB().Set([]byte("opv2:op:01HOLLOW00000000000000000000"), []byte("not json"), pebble.Sync))
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) { return kv.SweepHollowOperationsV2() },
		},
		{
			name: "PebbleStore.SweepBookFileSegDrop", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				// A legacy book_file row that still carries a fingerprint segment.
				require.NoError(t, kv.SetRaw("book_file:b:01LEGACY00000000000000000A",
					[]byte(`{"id":"01LEGACY00000000000000000A","book_id":"b","file_path":"/books/a.m4b","acoustid_seg0":"AQADtAcSRY"}`)))
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				res, err := kv.SweepBookFileSegDrop(ctx, false, 0, nil)
				return res.Rewrite, err
			},
		},
		{
			name: "PebbleMetricsStore.PruneCacheStatsHistory", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				ms := database.NewPebbleMetricsStore(kv.DB())
				snaps := make([]database.CacheStatsSnapshot, 5)
				for i := range snaps {
					snaps[i] = database.CacheStatsSnapshot{CacheName: "c", Timestamp: oldTime.Add(time.Duration(i) * time.Second), Hits: 1}
				}
				require.NoError(t, ms.RecordCacheStatsSnapshots(snaps))
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				n, err := database.NewPebbleMetricsStore(kv.DB()).PruneCacheStatsHistory(pruneCut)
				return int(n), err
			},
		},
		{
			name: "PebbleMetricsStore.SweepExpiredMetrics", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				require.NoError(t, database.NewPebbleMetricsStore(kv.DB()).RecordCacheStatsSnapshots(
					[]database.CacheStatsSnapshot{{CacheName: "expired", Timestamp: oldTime, Hits: 1}}))
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				n, err := database.NewPebbleMetricsStore(kv.DB()).SweepExpiredMetrics()
				return int(n), err
			},
		},
	}
	entries = append(entries, otherSweepEntries(ctx)...)
	for _, tier := range allTiers {
		entries = append(entries, retentionEntry{
			name: "PebbleActivityStore.Prune/" + tier, seed: seedActivity(tier), wantDeleted: true,
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				return database.NewPebbleActivityStore(kv.DB()).Prune(ctx, pruneCut, tier)
			},
		})
	}
	return entries
}

// otherSweepEntries are the sweeps that are not methods of the four reflected
// store types: expiry deletes on PebbleStore that the old name filter missed,
// and the sweeps that live on service and engine types.
func otherSweepEntries(ctx context.Context) []retentionEntry {
	return []retentionEntry{
		{
			name: "PebbleStore.DeleteExpiredSessions", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				_, err := kv.CreateSession("u-expired", "192.0.2.1", "ua", -time.Hour)
				require.NoError(t, err)
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) { return kv.DeleteExpiredSessions(time.Now()) },
		},
		{
			name: "PebbleStore.DeleteExpiredABSSessions", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				require.NoError(t, kv.CreateABSSession(&database.ABSSession{
					ID: "abs-expired", UserID: "u-expired", Revoked: true, CreatedAt: oldTime, ExpiresAt: oldTime,
				}))
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				return kv.DeleteExpiredABSSessions(time.Now())
			},
		},
		{
			name: "resultjournal.Journal.Prune", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				raw, err := json.Marshal(resultjournal.Entry{Endpoint: "e", Model: "m", At: oldTime, Result: json.RawMessage(`{}`)})
				require.NoError(t, err)
				require.NoError(t, kv.SetRaw("aijournal:retention:old", raw))
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				j, err := resultjournal.New(kv, "retention")
				require.NoError(t, err)
				return j.Prune(ctx, 24*time.Hour)
			},
		},
		{
			name: "AudiobookService.PurgeSoftDeletedBooks", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				yes, at := true, time.Now().Add(-time.Hour)
				_, err := kv.CreateBook(&database.Book{ID: "retention-trashed", Title: "Trashed", MarkedForDeletion: &yes, MarkedForDeletionAt: &at})
				require.NoError(t, err)
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				// The purge's book_file check is fail-closed until memdb serves.
				kv.WaitForWarmup()
				prev := config.AppConfig.RootDir
				t.Cleanup(func() { config.AppConfig.RootDir = prev })
				config.AppConfig.RootDir = t.TempDir()
				res, err := audiobooks.NewAudiobookService(kv).PurgeSoftDeletedBooks(ctx, false, nil)
				if err != nil {
					return 0, err
				}
				require.Empty(t, res.Errors)
				return res.Purged, nil
			},
		},
		{
			name: "dedup.Engine.PurgeStaleCandidates", wantDeleted: true,
			seed: func(t *testing.T, kv *database.PebbleStore) {
				// The chapter-file shape the same-directory rule purges.
				a, err := kv.CreateBook(&database.Book{Title: "Chapter Book", FilePath: "/lib/Author/Chapters/01.mp3"})
				require.NoError(t, err)
				b, err := kv.CreateBook(&database.Book{Title: "Chapter Book", FilePath: "/lib/Author/Chapters/02.mp3"})
				require.NoError(t, err)
				sim := 1.0
				_, _, err = database.NewEmbeddingStore(kv.DB()).UpsertCandidateNew(database.DedupCandidate{
					EntityType: "book", EntityAID: a.ID, EntityBID: b.ID, Layer: "embedding", Similarity: &sim,
				})
				require.NoError(t, err)
			},
			run: func(t *testing.T, kv *database.PebbleStore) (int, error) {
				eng, err := dedup.NewEngine(database.NewEmbeddingStore(kv.DB()), kv, nil, nil, nil, unified.DefaultScoreConfig())
				require.NoError(t, err)
				return eng.PurgeStaleCandidates(ctx)
			},
		},
	}
}

func TestRetention_EveryPruneEntryPointLeavesDecisionsIntact(t *testing.T) {
	for _, e := range retentionEntries() {
		t.Run(e.name, func(t *testing.T) {
			kv := newKV(t)
			if e.seed != nil {
				e.seed(t, kv)
			}
			before := seedDecisions(t, kv)
			n, err := e.run(t, kv)
			require.NoError(t, err)
			t.Logf("%s deleted %d rows", e.name, n)
			if e.wantDeleted {
				require.Positive(t, n, "positive control: %s must have pruned the old rows it was seeded with", e.name)
			}
			requireIntact(t, kv, before, e.name)
		})
	}
}

// The undo journal's survival is asserted by the existing opchange test; this
// pins that a run of EVERY entry point back to back, on one store, still
// leaves decisions untouched (an entry point that only misbehaves after
// another has run is not caught by the isolated runs above).
func TestRetention_AllEntryPointsInSequenceOnOneStore(t *testing.T) {
	kv := newKV(t)
	es := retentionEntries()
	for _, e := range es {
		if e.seed != nil {
			e.seed(t, kv)
		}
	}
	before := seedDecisions(t, kv)
	for _, e := range es {
		_, err := e.run(t, kv)
		require.NoError(t, err, e.name)
		requireIntact(t, kv, before, e.name)
	}
}

// retentionExempt are exported maintenance-named methods that are deliberately
// not in retentionEntries, each with the reason.
var retentionExempt = map[string]string{
	"Service.Prune": "activity.Service.Prune only delegates to PebbleActivityStore.Prune, which is covered per tier",
}

// pruneLikePrefixes are the method-name prefixes the canary treats as a
// retention job. DeleteExpired and Cleanup are here because the original
// Prune/Sweep/Purge filter missed DeleteExpiredSessions and
// DeleteExpiredABSSessions, which delete by age just the same.
var pruneLikePrefixes = []string{"Prune", "Sweep", "Purge", "DeleteExpired", "Cleanup"}

func pruneLike(method string) bool {
	for _, p := range pruneLikePrefixes {
		if strings.HasPrefix(method, p) {
			return true
		}
	}
	return false
}

// TestRetention_CanaryEveryPruneLikeMethodIsCovered fails when someone adds an
// exported Prune*/Sweep*/Purge*/DeleteExpired*/Cleanup* method to a store type. Add it to
// retentionEntries (with a positive control if its rows can be seeded) or to
// retentionExempt with a reason.
func TestRetention_CanaryEveryPruneLikeMethodIsCovered(t *testing.T) {
	covered := map[string]bool{}
	for _, e := range retentionEntries() {
		name := e.name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[:i]
		}
		covered[name] = true
	}
	var missing []string
	for _, typ := range []reflect.Type{
		reflect.TypeOf((*database.PebbleStore)(nil)),
		reflect.TypeOf((*database.PebbleActivityStore)(nil)),
		reflect.TypeOf((*database.PebbleMetricsStore)(nil)),
		reflect.TypeOf((*activity.Service)(nil)),
	} {
		short := strings.TrimPrefix(typ.String(), "*")
		short = short[strings.LastIndexByte(short, '.')+1:]
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i).Name
			if !pruneLike(m) {
				continue
			}
			q := short + "." + m
			if covered[q] {
				continue
			}
			if _, ok := retentionExempt[q]; ok {
				continue
			}
			missing = append(missing, q)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing, "new prune-like entry points must be added to retentionEntries (or retentionExempt) so a decision: survival test covers them")
	for q := range covered {
		require.NotEmpty(t, q)
	}
}

// Factory reset (PebbleStore.Reset) wipes the whole keyspace on purpose; it is
// the one documented exception, not a retention job.
func TestRetention_ResetIsTheOnlyWipe(t *testing.T) {
	kv := newKV(t)
	before := seedDecisions(t, kv)
	require.NotEmpty(t, before)
	require.NoError(t, kv.Reset())
	require.Empty(t, snapshotDecisions(t, kv), "Reset wipes everything by design")
}

// Only this package builds decision: keys. A literal anywhere else is a second
// writer (or deleter) the retention table above does not know about.
func TestRetention_NoOneElseWritesDecisionKeys(t *testing.T) {
	allowed := map[string]bool{
		"internal/database/keyfamilies.go": true, // the census registry names the prefixes
	}
	out := grepRepo(t, `"decision:`)
	var offenders []string
	for _, f := range out {
		if strings.HasPrefix(f, "internal/decisions/") || allowed[f] {
			continue
		}
		offenders = append(offenders, f)
	}
	require.Empty(t, offenders, "only internal/decisions may build decision: keys; anything else could delete or rewrite them")
}

// grepRepo lists the repo-relative non-test Go files under internal/ and cmd/
// whose text contains needle. It locates the repo with os.Getwd, not
// runtime.Caller: CI builds with -trimpath, which breaks the latter.
func grepRepo(t testing.TB, needle string) []string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		require.NotEqual(t, root, parent, "go.mod not found above %s", wd)
		root = parent
	}
	var hits []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(raw, []byte(needle)) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			require.NoError(t, err)
		}
	}
	// The guard must be able to see code at all, or it passes vacuously.
	require.NotEmpty(t, grepRepoControl(t, root), "grepRepo found no Go files")
	sort.Strings(hits)
	return hits
}

func grepRepoControl(t testing.TB, root string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, "internal", "decisions", "*.go"))
	require.NoError(t, err)
	return m
}
