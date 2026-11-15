// file: internal/plugins/maintenance/kinds_t6_g6_m5_test.go
// version: 1.2.4
// guid: 3e8b6d14-52a7-4c9f-b1d0-7a4f92c6e385
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Review item M5, the tests the first rounds left out: each one fails on the
// matching planted bug listed in scripts/mut/f511_mut.py of the mutation branch.

const t6g6HealPID = "BATPID0000000001"

// t6g6HealFixture writes one iTunes track whose mapped path is missing, with its
// file in the library under root under the same name: the heal's first layer
// resolves the single candidate. It returns the directory the track maps to.
func t6g6HealFixture(t *testing.T, root string) string {
	t.Helper()
	healDir := t.TempDir()
	writeFile(t, filepath.Join(root, "Gotham", "Hush.mp3"), 10)
	xml := filepath.Join(t.TempDir(), "Library.xml")
	require.NoError(t, os.WriteFile(xml, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>Tracks</key><dict><key>1</key><dict>
<key>Location</key><string>file://localhost/W:/Gotham/Hush.mp3</string>
<key>Name</key><string>Hush</string><key>Artist</key><string>Bruce Wayne</string>
<key>Persistent ID</key><string>`+t6g6HealPID+`</string></dict></dict></dict></plist>`), 0o644))
	oldITunes, oldRoot := config.AppConfig.ITunes, config.AppConfig.RootDir
	t.Cleanup(func() { config.AppConfig.ITunes, config.AppConfig.RootDir = oldITunes, oldRoot })
	config.AppConfig.ITunes.LibraryReadPath = xml
	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{{From: "W:/Gotham", To: healDir}}
	config.AppConfig.RootDir = root
	return healDir
}

// t6g6RunOnHost runs d's own Run on a host the test built (runT6G6Kind builds
// its own, and a test that resumes needs to seed a checkpoint first).
func t6g6RunOnHost(t *testing.T, d tasks.Definition, h *t6g6Host, params string) error {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task, "%s is not a Task", d.Kind())
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	in, err := decode(raw)
	require.NoError(t, err)
	_, err = task.Run(tasks.NewRC(h), in)
	return err
}

func newT6G6LiveHost(kind tasks.Kind, w tasks.Writer) *t6g6Host {
	return &t6g6Host{FakeRun: taskstest.NewFakeRun(context.Background(), kind), mode: tasks.ModeLive, w: w}
}

// ---- heal: the HEAD shell's own Run ----------------------------------------

// The HEAD shell (def.Run, what the registry calls) reflinks a missing track back
// through reconcile.RunITunesHeal over the plugin's own store.
func TestT6G6Head_HealRunReflinksMissingTrack(t *testing.T) {
	root := t.TempDir()
	healDir := t6g6HealFixture(t, root)
	p := &Plugin{deps: &fakeDeps{store: newRepairPebble(t), root: root}}

	require.NoError(t, p.itunesHealDef().Run(context.Background(), json.RawMessage(`{}`), &fakeReporter{}))
	_, err := os.Stat(filepath.Join(healDir, "Hush.mp3"))
	require.NoError(t, err, "the heal reflinked the missing track to its mapped path")
}

// The heal declares ai_journal because the journal its ambiguous-track resolver
// is handed writes aijournal: keys. The spy answers HealWhisperJournal with nil,
// and the transcription that calls Complete needs ffmpeg and a whisper pool, so
// this pins the production side of the same claim: the journal HEAD's writer
// hands the heal (reconcile.NewHeadHealWriter, which the native writer adapts)
// puts its result under the aijournal: root of a real store, and the kind's
// Effects declare the resource that names that root (stack review C11).
func TestT6G6Kinds_HealWhisperJournalWritesTheDeclaredAIJournalKeys(t *testing.T) {
	store := newRepairPebble(t)
	j := reconcile.NewHeadHealWriter(store).HealWhisperJournal()
	require.NotNil(t, j, "a store with a raw KV surface yields the whisper journal")

	before, _, err := store.ScanPrefixPage("", "", 1000)
	require.NoError(t, err)
	require.NoError(t, j.Complete("batclip-0001", "http://192.0.2.10:8000", "base", map[string]string{"text": "Year One"}))
	after, _, err := store.ScanPrefixPage("", "", 1000)
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, kv := range before {
		seen[kv.Key] = true
	}
	var written []string
	for _, kv := range after {
		if !seen[kv.Key] {
			written = append(written, kv.Key)
		}
	}
	require.NotEmpty(t, written, "Complete journals the result")
	for _, k := range written {
		require.True(t, strings.HasPrefix(k, "aijournal:whisper:"), "the heal's journal writes only under the ai_journal keyspace (pkg/ops.ResAIJournal), wrote %q", k)
	}

	def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(newT6G6Probe(t).deps(), nil), itunesHealKind)
	declared := map[string]bool{}
	for _, res := range tasks.CommonOf(def).Effects.WriteSet() {
		declared[tasks.FormatRef(res)] = true
	}
	require.True(t, declared["books/"+string(ops.ResAIJournal)], "%s writes aijournal: keys but does not declare %s", itunesHealKind, ops.ResAIJournal)
}

// ---- param pass-throughs ----------------------------------------------------

// Every param a native shell decodes reaches its core. Each row runs the kind
// live with and without the param and compares what the writer was handed.
func TestT6G6Native_ParamsReachTheCores(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}
	run := func(t *testing.T, setup func(*testing.T, *t6g6Probe), kind tasks.Kind, params string) []string {
		t.Helper()
		p := newT6G6Probe(t)
		setup(t, p)
		_, err := runT6G6Kind(t, t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), kind), tasks.ModeLive, gate, params)
		require.NoError(t, err)
		return p.rec.snapshot()
	}

	t.Run("heal last_processed_pid skips the track it names", func(t *testing.T) {
		setup := func(t *testing.T, p *t6g6Probe) { t6g6HealFixture(t, p.root) }
		require.Equal(t, []string{"HealTrack Hush.mp3 <- Hush.mp3"}, run(t, setup, itunesHealKind, ``))
		require.Empty(t, run(t, setup, itunesHealKind, `{"last_processed_pid":"`+t6g6HealPID+`"}`))
	})

	t.Run("transcribe last_book_id resumes past the book it names", func(t *testing.T) {
		const transportErr = `transcribe remote: transcribe-batch chunk 0-32: Post "http://<host>/transcribe-batch": dial tcp: connect: connection refused`
		setup := func(t *testing.T, p *t6g6Probe) {
			s := &t6g6TestStore{}
			p.rec.books = map[string]*database.Book{}
			for _, id := range []string{"01BATBOOK00000000000000D1", "01BATBOOK00000000000000D2"} {
				b := database.Book{ID: id, TranscribeStatus: new(statusWhisperError), TranscribeError: new(transportErr)}
				s.add(b)
				p.rec.books[id] = &b
			}
			p.store = s
		}
		require.Equal(t, []string{
			"ModifyBook 01BATBOOK00000000000000D1 status=cleared",
			"ModifyBook 01BATBOOK00000000000000D2 status=cleared",
		}, t6g6Sorted(run(t, setup, repairTranscribeStatusKind, ``)))
		require.Equal(t, []string{"ModifyBook 01BATBOOK00000000000000D2 status=cleared"},
			run(t, setup, repairTranscribeStatusKind, `{"last_book_id":"01BATBOOK00000000000000D1"}`))
	})

	t.Run("relink limit caps the repairs", func(t *testing.T) {
		setup := func(t *testing.T, p *t6g6Probe) {
			s := &t6g6TestStore{}
			for i, title := range []string{"The Long Halloween", "Dark Victory"} {
				path := filepath.Join(t.TempDir(), title+".mp3")
				require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
				s.add(database.Book{ID: "01BATBOOK00000000000000B" + itoa(i+1), Title: title, FilePath: path})
			}
			p.store = s
		}
		require.Len(t, run(t, setup, relinkUnlinkedBooksKind, ``), 2)
		require.Len(t, run(t, setup, relinkUnlinkedBooksKind, `{"limit":1}`), 1)
	})

	t.Run("probe limit caps the books linked", func(t *testing.T) {
		setup := func(t *testing.T, p *t6g6Probe) {
			sec := map[string]float64{}
			s := &t6g6TestStore{}
			for i, stem := range []string{"The Killing Joke", "Dark Victory"} {
				var names []string
				for _, n := range []string{"_01_.mp3", "_02_.mp3", "_03_.mp3"} {
					names = append(names, stem+n)
					sec[stem+n] = 1500
				}
				s.add(database.Book{ID: "01BATBOOK00000000000000C" + itoa(i+1), Title: stem, FilePath: makeFolder(t, names...)})
			}
			installFFprobeLookup(t, "ffprobe", nil)
			installProber(t, &fakeProber{bySec: sec})
			p.store = s
		}
		require.Len(t, run(t, setup, probeDirectoryBooksKind, `{}`), 6)
		require.Len(t, run(t, setup, probeDirectoryBooksKind, `{"limit":1}`), 3)
	})

	t.Run("recover max caps the repoints", func(t *testing.T) {
		setup := func(t *testing.T, p *t6g6Probe) { p.store = t6g6RecoverStore(t, p.root, 2, ".mp3") }
		require.Len(t, run(t, setup, recoverMissingFilesKind, ``), 2)
		require.Len(t, run(t, setup, recoverMissingFilesKind, `{"max":1}`), 1)
	})

	t.Run("recover requireExtMatch false accepts a renamed extension", func(t *testing.T) {
		setup := func(t *testing.T, p *t6g6Probe) { p.store = t6g6RecoverStore(t, p.root, 1, ".m4a") }
		require.Empty(t, run(t, setup, recoverMissingFilesKind, ``), "an extension mismatch is refused by default")
		require.Len(t, run(t, setup, recoverMissingFilesKind, `{"requireExtMatch":false}`), 1)
	})

	t.Run("recover reportPath reaches the report resolver", func(t *testing.T) {
		p := newT6G6Probe(t)
		p.store = t6g6RecoverStore(t, p.root, 1, ".mp3")
		deps := p.deps()
		var got []string
		deps.ResolveReportPath = func(explicit, _ string) (string, error) {
			got = append(got, explicit)
			return p.reportPath, nil
		}
		def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(deps, nil), recoverMissingFilesKind)
		_, err := runT6G6Kind(t, def, tasks.ModeLive, gate, `{"reportPath":"/gotham/report.tsv"}`)
		require.NoError(t, err)
		require.Equal(t, []string{"/gotham/report.tsv"}, got)
	})

	t.Run("relink includeDirectories repairs a directory-shaped book", func(t *testing.T) {
		setup := func(t *testing.T, p *t6g6Probe) {
			s := &t6g6TestStore{}
			s.add(database.Book{ID: "01BATBOOK00000000000000F1", Title: "Arkham Asylum",
				FilePath: makeFolder(t, "Chapter 01.mp3")})
			p.store = s
		}
		require.Empty(t, run(t, setup, relinkUnlinkedBooksKind, ``), "a directory-shaped book is skipped by default")
		require.Equal(t, []string{"CreateBookFile Chapter 01.mp3"},
			run(t, setup, relinkUnlinkedBooksKind, `{"includeDirectories":true}`))
	})

	t.Run("recover sourceDirs and reflinkOutside reach the plan", func(t *testing.T) {
		// One row whose bytes exist only under a source dir. The report TSV names
		// the bucket the plan put the row in.
		bucketOf := func(t *testing.T, params func(src string) string) string {
			t.Helper()
			p := newT6G6Probe(t)
			src := t.TempDir()
			const size = 4301
			writeFile(t, filepath.Join(src, "Hush", "renamed.mp3"), size)
			gone := filepath.Join(p.root, "Gotham", "Hush", "gone.mp3")
			bookID, fileID := "01BATBOOK00000000000000F2", "01BATFILE00000000000000F2"
			s := &t6g6TestStore{files: map[string][]database.BookFile{}}
			s.cores = append(s.cores, database.BookFileCore{ID: fileID, BookID: bookID, FilePath: gone, FileSize: size})
			s.files[bookID] = []database.BookFile{{ID: fileID, BookID: bookID, FilePath: gone, FileSize: size}}
			p.store = s
			_, err := runT6G6Kind(t, t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), recoverMissingFilesKind), tasks.ModePreview, nil, params(src))
			require.NoError(t, err)
			b, err := os.ReadFile(p.reportPath)
			require.NoError(t, err)
			return string(b)
		}
		require.Contains(t, bucketOf(t, func(string) string { return `{}` }), "\nnowhere\t",
			"without a source dir the bytes are nowhere")
		require.Contains(t, bucketOf(t, func(src string) string { return `{"sourceDirs":[` + strconv.Quote(src) + `]}` }), "\noutside\t",
			"sourceDirs finds the bytes outside the library")
		require.Contains(t, bucketOf(t, func(src string) string {
			return `{"sourceDirs":[` + strconv.Quote(src) + `],"reflinkOutside":true}`
		}), "\nreflinkable\t", "reflinkOutside turns the outside match into a reflink")
	})

	t.Run("regroup limit stops the stale purge", func(t *testing.T) {
		setup := func(t *testing.T, p *t6g6Probe) { p.store = t6g6StaleHoldStore(t) }
		require.Contains(t, run(t, setup, regroupShatteredAIKind, ``), "DeleteReviewItemIfPending 01BATHOLD0000000000000S1")
		require.NotContains(t, run(t, setup, regroupShatteredAIKind, `{"limit":1}`), "DeleteReviewItemIfPending 01BATHOLD0000000000000S1")
	})
}

func t6g6Sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// t6g6RecoverStore is a library of n book_file rows whose files are gone from
// their recorded path and sit renamed (extension ext) in the same folder, each
// with its own size so the size match is unambiguous.
func t6g6RecoverStore(t *testing.T, root string, n int, ext string) *t6g6TestStore {
	t.Helper()
	s := &t6g6TestStore{files: map[string][]database.BookFile{}}
	for i := 1; i <= n; i++ {
		dir := filepath.Join(root, "Gotham", "Dark Victory "+itoa(i))
		gone := filepath.Join(dir, "gone.mp3")
		size := int64(4200 + i)
		writeFile(t, filepath.Join(dir, "renamed"+ext), int(size))
		fileID, bookID := "01BATFILE00000000000000E"+itoa(i), "01BATBOOK00000000000000E"+itoa(i)
		s.cores = append(s.cores, database.BookFileCore{ID: fileID, BookID: bookID, FilePath: gone, FileSize: size})
		s.files[bookID] = []database.BookFile{{ID: fileID, BookID: bookID, FilePath: gone, FileSize: size}}
	}
	return s
}

// ---- regroup: the stale-holds purger ---------------------------------------

// t6g6HoldStore is a shattered library plus a review queue with the pending
// holds a test seeds.
type t6g6HoldStore struct {
	*t6g6TestStore
	pending []database.ReviewItem
}

func (s t6g6HoldStore) ListReviewItems(database.ReviewFilter) ([]database.ReviewItem, int, error) {
	return s.pending, len(s.pending), nil
}

// t6g6StaleHoldStore holds a regroup hold for a folder the run no longer emits
// (the stale one) and another producer's pending hold, which is never purged.
func t6g6StaleHoldStore(t *testing.T) t6g6HoldStore {
	t.Helper()
	return t6g6HoldStore{
		t6g6TestStore: t6g6ShatterStore(t),
		pending: []database.ReviewItem{
			{ID: "01BATHOLD0000000000000S1", Kind: "regroup.multidisc", DedupKey: "regroup:0000stale", FolderRef: "/lib/Bruce Wayne/Gone", Status: database.ReviewStatusPending},
			{ID: "01BATHOLD0000000000000O1", Kind: "dedup.pair", DedupKey: "dedup:0000other", FolderRef: "/lib/Selina Kyle", Status: database.ReviewStatusPending},
		},
	}
}

// A live regroup upserts the folder it emits and deletes, through the gated
// writer, the pending regroup hold it no longer emits; the other producer's
// pending hold stays.
func TestT6G6Native_RegroupPurgesStaleHoldsThroughWriter(t *testing.T) {
	p := newT6G6Probe(t)
	p.store = t6g6StaleHoldStore(t)
	def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), regroupShatteredAIKind)
	gate := &t6g6GateWriter{name: "gotham"}

	_, err := runT6G6Kind(t, def, tasks.ModeLive, gate, ``)
	require.NoError(t, err)
	require.Equal(t, []string{
		"UpsertReviewItem /lib/Bruce Wayne/Year One",
		"DeleteReviewItemIfPending 01BATHOLD0000000000000S1",
	}, p.rec.snapshot())
	require.Same(t, gate, p.given, "the purge's delete goes through the writer built over the run's writer")
	t6g6AssertWritesDeclared(t, p.rec.snapshot(), def)

	// A preview deletes nothing.
	p = newT6G6Probe(t)
	p.store = t6g6StaleHoldStore(t)
	_, err = runT6G6Kind(t, t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), regroupShatteredAIKind), tasks.ModePreview, nil, ``)
	require.NoError(t, err)
	require.Empty(t, p.rec.snapshot())
}

// ---- opchange: the native ensure -------------------------------------------

// ensure=true runs the store's ensure (and not the rebuild), asking for a yield
// margin, and is a write mode a Preview run refuses.
func TestT6G6OpChange_EnsureRunsTheEnsureNotTheRebuild(t *testing.T) {
	p := newT6G6Probe(t)
	var opts []database.OpChangeEnsureOptions
	p.rec.ensure = func(o database.OpChangeEnsureOptions) (database.OpChangeByBookEnsureResult, error) {
		opts = append(opts, o)
		return database.OpChangeByBookEnsureResult{Trusted: true, Backfill: database.OpChangeByBookBackfillResult{Skipped: true}}, nil
	}
	def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), opChangeBookIndexKind)

	_, err := runT6G6Kind(t, def, tasks.ModeLive, &t6g6GateWriter{name: "gotham"}, `{"ensure":true}`)
	require.NoError(t, err)
	require.Equal(t, []string{"EnsureOpChangeByBookIndexWith"}, p.rec.snapshot())
	require.Len(t, opts, 1)
	require.Positive(t, opts[0].YieldMargin, "the ensure asks to yield before the attempt's deadline")
	t6g6AssertWritesDeclared(t, p.rec.snapshot(), def)

	p = newT6G6Probe(t)
	_, err = runT6G6Kind(t, t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), opChangeBookIndexKind), tasks.ModePreview, nil, `{"ensure":true}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ensure=true writes index keys")
	require.Zero(t, p.writerCalls(), "a refused preview built a writer")
}

// A verify that ends at its time budget yields: the native shell writes the
// marker the kernel needs, then returns tasks.ErrContinueInPlace. The next
// attempt (the marker is in the checkpoint; the resume point itself is the
// store's cursor, which the core never reads back) re-enters the ensure and
// finishes. The core calls Checkpoint and never Restore.
func TestT6G6OpChange_YieldWritesMarkerAndSecondAttemptReentersEnsure(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}
	p := newT6G6Probe(t)
	attempt := 0
	p.rec.ensure = func(database.OpChangeEnsureOptions) (database.OpChangeByBookEnsureResult, error) {
		attempt++
		if attempt == 1 {
			return database.OpChangeByBookEnsureResult{Yielded: true, Verify: database.OpChangeByBookIndexReport{Rows: 7, VerifiedRows: 7}}, nil
		}
		return database.OpChangeByBookEnsureResult{Trusted: true}, nil
	}
	def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), opChangeBookIndexKind)

	h1 := newT6G6LiveHost(opChangeBookIndexKind, gate)
	err := t6g6RunOnHost(t, def, h1, `{"ensure":true}`)
	require.ErrorIs(t, err, tasks.ErrContinueInPlace)
	raw, ok := h1.CheckpointJSON("")
	require.True(t, ok, "the yield wrote a checkpoint")
	require.JSONEq(t, `{"verified_rows":7}`, string(raw))

	h2 := newT6G6LiveHost(opChangeBookIndexKind, gate)
	require.NoError(t, h2.SetCheckpoint("", json.RawMessage(raw)))
	require.NoError(t, t6g6RunOnHost(t, def, h2, `{"ensure":true}`))
	require.Equal(t, 2, attempt, "the second attempt re-entered the ensure")
	require.Equal(t, []string{"EnsureOpChangeByBookIndexWith", "EnsureOpChangeByBookIndexWith"}, p.rec.snapshot())
}

// ---- parallelism ------------------------------------------------------------

// Every native kind with a pool of its own runs it at the worker count the kind
// declares, hand-written here (CPU() is NumCPU): the host sees each loop's
// declared count before the operator cap.
func TestT6G6Native_ShellsRunTheirPoolAtTheDeclaredParallelism(t *testing.T) {
	cpu := runtime.NumCPU()
	want := map[tasks.Kind]int{
		itunesHealKind:             16,
		relinkUnlinkedBooksKind:    cpu,
		probeDirectoryBooksKind:    cpu,
		repairTranscribeStatusKind: 8,
		regroupShatteredAIKind:     cpu,
		recoverMissingFilesKind:    24,
	}
	gate := &t6g6GateWriter{name: "gotham"}
	seen := 0
	for _, c := range t6g6WriteCases() {
		workers, ok := want[c.kind]
		if !ok {
			continue
		}
		seen++
		t.Run(string(c.kind), func(t *testing.T) {
			p := newT6G6Probe(t)
			if c.setup != nil {
				c.setup(t, p)
			}
			h, err := runT6G6Kind(t, t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), c.kind), tasks.ModeLive, gate, c.params)
			require.NoError(t, err)
			caps := h.workerCaps()
			require.NotEmpty(t, caps, "the run never started an item loop")
			for _, got := range caps {
				require.Equal(t, workers, got, "declared workers of a loop")
			}
		})
	}
	require.Equal(t, len(want), seen, "every pooled kind has a write case to run")
}

// The orphan repoint plan's classification pool is a CPU() pool, on either
// shell: the shared core hands the pool this parallelism.
func TestT6G6Core_OrphanPlanPoolIsCPU(t *testing.T) {
	store := newPlanStore(t, true)
	var got []tasks.Parallelism
	items := func(ctx context.Context, par tasks.Parallelism, n int, o t6g6ItemsOptions, fn func(ctx context.Context, i int) error) error {
		got = append(got, par)
		return t6g6ErrgroupItems(ctx, par, n, o, fn)
	}
	_, err := t6g6PlanOrphanRepointsVia(context.Background(), store, []database.BookFileCore{
		{ID: "o-none", BookID: "ghost-3", FilePath: "/nowhere/n.mp3"},
	}, items)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, got[0].UsesCPUBudget(), "orphan plan pool %s, want CPU()", got[0])
	require.Equal(t, runtime.NumCPU(), got[0].Workers())
}

// ---- schedule hook ----------------------------------------------------------

// The schedule a kind runs on is the registration's to give: t6g6Kinds asks the
// hook once per kind and the answer lands on that definition; a nil hook or a
// nil answer leaves it unscheduled.
func TestT6G6Kinds_ScheduleHookReachesEveryDefinition(t *testing.T) {
	asked := map[tasks.Kind]int{}
	keys := map[tasks.Kind]string{}
	hook := func(k tasks.Kind) *tasks.Schedule {
		asked[k]++
		keys[k] = "modules.books.scheduler.schedules.k" + itoa(len(keys))
		return tasks.ScheduleFromConfig(keys[k])
	}
	defs := (&Plugin{}).t6g6Kinds(fakeT6G6Deps(t), hook)
	require.Len(t, defs, 14)
	for _, d := range defs {
		require.Equal(t, 1, asked[d.Kind()], "%s: hook calls", d.Kind())
		s := tasks.CommonOf(d).Schedule
		require.NotNil(t, s, "%s has no schedule", d.Kind())
		require.Equal(t, keys[d.Kind()], s.ConfigKey(), "%s schedule", d.Kind())
	}

	for name, h := range map[string]func(tasks.Kind) *tasks.Schedule{
		"nil hook":   nil,
		"nil answer": func(tasks.Kind) *tasks.Schedule { return nil },
	} {
		for _, d := range (&Plugin{}).t6g6Kinds(fakeT6G6Deps(t), h) {
			require.Nil(t, tasks.CommonOf(d).Schedule, "%s: %s must be unscheduled", name, d.Kind())
		}
	}
}

// ---- optimize: the child kinds ----------------------------------------------

// The four children the sweep launches are named by constants, and the kind a
// sibling row must declare for each is "books." + the HEAD def ID. The one
// HEAD def this package owns is checked against its constant here; the acoustid
// defs live in another package (their literals are recorded in seams.d/F5-11.md
// for the row that converts them).
func TestT6G6Optimize_ChildKindsAreTheLaunchedConstants(t *testing.T) {
	require.Equal(t, "maintenance.temp-file-cleanup", optimizeChildTempFileCleanup)
	require.Equal(t, (&Plugin{}).tempFileCleanupDef().ID, optimizeChildTempFileCleanup, "the def the sweep launches")
	require.Equal(t, tasks.Kind("books.maintenance.temp-file-cleanup"), t6g6KindFor(optimizeChildTempFileCleanup))
	require.Equal(t, tasks.Kind("books.acoustid.fingerprint-rescan"), t6g6KindFor(optimizeChildFingerprintRescan))
	require.Equal(t, tasks.Kind("books.acoustid.scan"), t6g6KindFor(optimizeChildAcoustIDScan))
	require.Equal(t, tasks.Kind("books.acoustid.backfill"), t6g6KindFor(optimizeChildAcoustIDBackfill))
}

// HEAD decoded the repoint params strictly. Every payload below is otherwise
// valid (the proven repoint fixture's own item), so a plain json.Unmarshal would
// accept it: only the strict decode can refuse it, and the control row (the same
// item, no loose key) runs the repoint to the end.
//
// Two layers refuse. The kind's own decodeStrictParams refuses the unknown key,
// the duplicate key and the unknown field ("decode params"). Data after the
// object never reaches the kind: the executor's decode of the start request
// (json.Unmarshal into the raw params, core/tasks/erased.go) refuses it first, so
// that row asserts the executor's refusal and that nothing ran. Either way HEAD's
// "unexpected data after the params object" refusal holds.
func TestT6G6Native_RepointRefusesLooseParams(t *testing.T) {
	gate := &t6g6GateWriter{name: "gotham"}
	run := func(t *testing.T, params func(f rbfFixture) string) (*t6g6Probe, error) {
		f := newRBFFixture(t, -1, "-")
		p := newT6G6Probe(t)
		p.store = t6g6RBFStore{s: f.s}
		p.rec.backing = f.s
		def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), rbfKind)
		_, err := runT6G6KindRefusing(t, def, tasks.ModeLive, gate, params(f))
		return p, err
	}
	item := func(f rbfFixture) string {
		b, err := json.Marshal(f.item())
		require.NoError(t, err)
		return string(b)
	}

	p, err := run(t, func(f rbfFixture) string { return `{"repoints":[` + item(f) + `]}` })
	require.NoError(t, err, "the control payload applies")
	require.NotEmpty(t, p.rec.snapshot())

	for name, c := range map[string]struct {
		params func(f rbfFixture) string
		want   string // a substring of the refusal
	}{
		"unknown key":   {func(f rbfFixture) string { return `{"repoints":[` + item(f) + `],"bogus":1}` }, "decode params"},
		"duplicate key": {func(f rbfFixture) string { return `{"repoints":[` + item(f) + `],"repoints":[` + item(f) + `]}` }, "decode params"},
		"trailing data": {func(f rbfFixture) string { return `{"repoints":[` + item(f) + `]} {}` }, "do not decode"},
		"unknown field": {func(f rbfFixture) string {
			it := item(f)
			return `{"repoints":[` + it[:len(it)-1] + `,"bogus":1}]}`
		}, "decode params"},
	} {
		p, err := run(t, c.params)
		require.Error(t, err, name)
		require.Contains(t, err.Error(), c.want, name)
		require.Empty(t, p.rec.snapshot(), name+" wrote")
	}
}

// Native pools carry the item-only progress labels HEAD set (the counter labels
// are a recorded seam): each pure label is handed to tasks.RunItems.
func TestT6G6Native_PoolForwardsThePureLabel(t *testing.T) {
	h := newT6G6LiveHost(relinkUnlinkedBooksKind, nil)
	var got []string
	require.NoError(t, t6g6NativeItems(tasks.NewRC(h))(context.Background(), tasks.Sequential("test"), 2,
		t6g6ItemsOptions{PureLabel: func(i, total int) string {
			s := "item " + itoa(i+1) + "/" + itoa(total)
			got = append(got, s)
			return s
		}},
		func(context.Context, int) error { return nil }))
	require.ElementsMatch(t, []string{"item 1/2", "item 2/2"}, got)

	// HEAD's label gets the absolute ProgressTotal when the loop sets one.
	got = nil
	require.NoError(t, t6g6NativeItems(tasks.NewRC(h))(context.Background(), tasks.Sequential("test"), 2,
		t6g6ItemsOptions{ProgressBase: 8, ProgressTotal: 10, PureLabel: func(i, total int) string {
			s := "item " + itoa(i+1) + "/" + itoa(total)
			got = append(got, s)
			return s
		}},
		func(context.Context, int) error { return nil }))
	require.ElementsMatch(t, []string{"item 1/10", "item 2/10"}, got)
}
