// file: internal/plugins/maintenance/kinds_t6_g4_parity_test.go
// version: 1.0.0
// guid: 1f3e20f0-c667-44b8-9f41-3e7208c9958c
// last-edited: 2026-11-14

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/mediacore/tasks"
)

// Ruling 2 of the common maintenance decisions: every writing definition has a
// HEAD-shell def.Run test over a fixture plugin that proves its writes reach the
// store, and a native live test that proves the writer factory received
// rc.Writer() and that each write reached the recorder. This table runs BOTH on
// the same fixture, shape by shape:
//
//   - HEAD's own def.Run runs against a freshly seeded store and must change it;
//   - the native kind runs against an identically seeded store through a spy
//     writer, and must leave the store in the SAME observed state, record exactly
//     the resources the case lists (and not a subset of what the Effects declare),
//     and build its writer from this run's rc.Writer().
//
// observe renders only what the case cares about and nothing that varies between
// two seeds (no generated IDs, no temp-dir paths).

// t6g4LiveEnv is one seeded fixture.
type t6g4LiveEnv struct {
	p       *Plugin
	observe func() string
	// headParams and params override the case's when a seed's params name a path
	// of its own (a temp root, an output file).
	headParams, params string
}

func (e *t6g4LiveEnv) paramsFor(head bool, c t6g4LiveCase) string {
	switch {
	case head && e.headParams != "":
		return e.headParams
	case head:
		return c.headParams
	case e.params != "":
		return e.params
	}
	return c.params
}

type t6g4LiveCase struct {
	kind       tasks.Kind
	headParams string
	params     string
	// want is the set of resources the run's writer calls must record, exactly.
	want []ops.Resource
	// unrecorded are declared resources the run writes outside the writer (a file
	// on disk), so no writer call can record them.
	unrecorded []ops.Resource
	// partial, when set, names why the fixture reaches only part of the declared
	// write set (and which part is therefore unproven); the declaration check is
	// then recorded-subset-of-declared instead of equality.
	partial string
	seed    func(t *testing.T) *t6g4LiveEnv
}

var t6g4LiveCases []t6g4LiveCase

func t6g4HeadDefOf(t *testing.T, p *Plugin, kind tasks.Kind) func(context.Context, json.RawMessage) error {
	t.Helper()
	for _, w := range t6g4Expected {
		if w.kind == kind {
			def := w.head(p)
			return func(ctx context.Context, raw json.RawMessage) error {
				return def.Run(ctx, raw, &fileIntroRep{id: "run-head-shell"})
			}
		}
	}
	t.Fatalf("no HEAD definition for %s in t6g4Expected", kind)
	return nil
}

// TestT6G4Kinds_LiveRunsMatchHEADAndRecordTheirWrites is the table runner.
func TestT6G4Kinds_LiveRunsMatchHEADAndRecordTheirWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	for _, c := range t6g4LiveCases {
		t.Run(string(c.kind), func(t *testing.T) {
			head := c.seed(t)
			before := head.observe()
			if err := t6g4HeadDefOf(t, head.p, c.kind)(context.Background(), json.RawMessage(head.paramsFor(true, c))); err != nil {
				t.Fatalf("HEAD def.Run: %v", err)
			}
			afterHead := head.observe()
			if afterHead == before {
				t.Fatalf("HEAD's def.Run changed nothing the case observes; the fixture proves nothing:\n%s", before)
			}

			nat := c.seed(t)
			if got := nat.observe(); got != before {
				t.Fatalf("the two seeds differ before the run:\n%s\nvs\n%s", before, got)
			}
			rec := &t6g4Calls{}
			var handed []tasks.Writer
			def := t6g4KindByName(t, t6g4Kinds(t6g4RealDeps(nat.p, rec, &handed), nil), c.kind)
			run := &t6g4RunWriter{name: "native"}
			h, cancel := t6g4RealHost(c.kind, 0, run)
			defer cancel()
			if _, err := runT6G4Kind(t, def, tasks.NewRC(h), nat.paramsFor(false, c)); err != nil {
				t.Fatalf("native run: %v", err)
			}
			if got := nat.observe(); got != afterHead {
				t.Errorf("native run left the store as\n%s\nHEAD's def.Run left it as\n%s", got, afterHead)
			}
			if len(handed) != 1 || handed[0] != tasks.Writer(run) {
				t.Errorf("NewWriter was handed %v, want this run's rc.Writer() once", handed)
			}
			t6g4SameResources(t, rec, c.want...)

			// The declaration is exactly what the run touches: what the writer
			// recorded plus what the case names as written outside it.
			declared := map[string]bool{}
			for _, r := range tasks.CommonOf(def).Effects.WriteSet() {
				declared[r.Kind] = true
			}
			expect := map[string]bool{}
			for _, r := range append(slices.Clone(c.want), c.unrecorded...) {
				expect[string(r)] = true
			}
			switch {
			case c.partial != "":
				for r := range expect {
					if !declared[r] {
						t.Errorf("the live run touches %q, which the Effects do not declare (%v)", r, t6g4Keys(declared))
					}
				}
				t.Logf("partial fixture (%s): declared %v, exercised %v", c.partial, t6g4Keys(declared), t6g4Keys(expect))
			case !slices.Equal(t6g4Keys(declared), t6g4Keys(expect)):
				t.Errorf("declared write set %v, but the live run touches %v", t6g4Keys(declared), t6g4Keys(expect))
			}
		})
	}
}

func t6g4Keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func t6g4MemStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Writes made before warmup publishes are dropped from memdb, and the cores
	// read the memdb projections; without this a run sees an empty library.
	s.WaitForWarmup()
	return s
}

// t6g4ObserveBooks renders every book and its rows, ordered by title and path.
func t6g4ObserveBooks(t *testing.T, s *database.PebbleStore) string {
	t.Helper()
	ids, err := s.ListBookIDs()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, id := range ids {
		b, err := s.GetBookByID(id)
		if err != nil || b == nil {
			t.Fatalf("GetBookByID(%s): %v %v", id, b, err)
		}
		dur := -1
		if b.Duration != nil {
			dur = *b.Duration
		}
		tr := ""
		if b.IntroTranscription != nil {
			tr = *b.IntroTranscription
		}
		var rows []string
		files, err := s.GetBookFiles(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			ft := ""
			if f.IntroTranscription != nil {
				ft = *f.IntroTranscription
			}
			wins, werr := s.GetFingerprintWindows(database.FileWindowRef(f.ID))
			if werr != nil {
				t.Fatal(werr)
			}
			rows = append(rows, fmt.Sprintf("{%s dur=%d intro=%q windows=%d}", f.FilePath, f.Duration, ft, len(wins)))
		}
		slices.Sort(rows)
		status := ""
		if b.TranscribeStatus != nil {
			status = *b.TranscribeStatus
		}
		lines = append(lines, fmt.Sprintf("%s dur=%d intro=%q status=%q rows=%s", b.Title, dur, tr, status, strings.Join(rows, ",")))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func init() {
	// dedupe-book-file-rows: two books of three copies each, every copy with a
	// fingerprint window, so the run deletes rows (book_files), carries windows
	// (fingerprint_index), journals each deletion (operations) and recomputes the
	// aggregates (books).
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: dedupeBookFileRowsKind, headParams: `{"apply":true}`, params: `{}`,
		want: []ops.Resource{ops.ResBookFiles, ops.ResBooks, ops.ResFingerprintIndex, ops.ResOperations},
		seed: func(t *testing.T) *t6g4LiveEnv {
			s := t6g4MemStore(t)
			for _, id := range seedDupBooks(t, s, 2, 3) {
				files, err := s.GetBookFiles(id)
				if err != nil {
					t.Fatal(err)
				}
				for i := range files {
					w := &database.FingerprintWindow{
						Ref: database.FileWindowRef(files[i].ID), Kind: database.WindowKindWindow,
						SlotBP: 1000 * (i + 1), Pipeline: "ffpcm-s16le-11025-mono/v1",
						Raw: bytes.Repeat([]byte{byte(i + 1)}, 4*100),
					}
					if err := s.PutFingerprintWindow(w); err != nil {
						t.Fatal(err)
					}
				}
			}
			p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: s}, root: t.TempDir()}}
			return &t6g4LiveEnv{p: p, observe: func() string { return t6g4ObserveBooks(t, s) }}
		},
	})

	// duration-backfill, fingerprint pass. Book one has a segment whose stored
	// duration is far off its fingerprint duration: the run rewrites that row in one
	// UpdateBookFiles call (book_files). Book two has segments within tolerance but
	// a wrong total: the run only recomputes its aggregates (books).
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: durationBackfillKind, headParams: `{"dryRun":false}`, params: `{}`,
		want: []ops.Resource{ops.ResBookFiles, ops.ResBooks},
		seed: func(t *testing.T) *t6g4LiveEnv {
			s := t6g4MemStore(t)
			for _, bk := range []struct {
				title string
				files []*database.BookFile
			}{
				{"The Long Halloween", []*database.BookFile{
					{FilePath: "/nonexistent/lh-01.mp3", Duration: database.Secs(50), AcoustIDFingerprintDurationSec: 1800},
					{FilePath: "/nonexistent/lh-02.mp3", Duration: database.Secs(1801), AcoustIDFingerprintDurationSec: 1800},
				}},
				{"Dark Victory", []*database.BookFile{
					{FilePath: "/nonexistent/dv-01.mp3", Duration: database.Secs(1801), AcoustIDFingerprintDurationSec: 1800},
				}},
			} {
				b, err := s.CreateBook(&database.Book{Title: bk.title, FilePath: "/lib/" + bk.title, Duration: new(120)})
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range bk.files {
					f.BookID = b.ID
					if err := s.CreateBookFile(f); err != nil {
						t.Fatal(err)
					}
				}
			}
			return &t6g4LiveEnv{p: New(fakeDeps{store: s}), observe: func() string { return t6g4ObserveBooks(t, s) }}
		},
	})

	// intro-migrate-single-file: a one-file book carrying a book-level transcript
	// and a file with none; the run copies the transcript onto the row.
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: introMigrateSingleFileKind, headParams: `{"dry_run":false}`, params: `{}`,
		want: []ops.Resource{ops.ResBookFiles},
		seed: func(t *testing.T) *t6g4LiveEnv {
			s := t6g4MemStore(t)
			tr := "This is Gotham Central, a Batman audiobook narrated by Alfred Pennyworth."
			b, err := s.CreateBook(&database.Book{Title: "Year One", IntroTranscription: &tr})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: "/lib/year-one/01.mp3", Duration: database.Secs(60)}); err != nil {
				t.Fatal(err)
			}
			return &t6g4LiveEnv{p: New(fakeDeps{store: s}), observe: func() string { return t6g4ObserveBooks(t, s) }}
		},
	})

	// dedup-exact-triage: the apply is a service call on the server deps; the
	// observable change is that the service was asked to apply.
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: dedupExactTriageKind, headParams: `{"apply":true}`, params: `{}`,
		want: []ops.Resource{ops.ResEmbeddings},
		seed: func(t *testing.T) *t6g4LiveEnv {
			d := &triageDeps{}
			p := newTriagePlugin(d)
			return &t6g4LiveEnv{p: p, observe: func() string {
				return fmt.Sprintf("service called=%v apply=%v", d.applyCalled, d.gotApply)
			}}
		},
	})

	// file-provenance-capture: a root with two files; the run hashes them and
	// appends one orphan observation per file to the ledger.
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: fileProvenanceCaptureKind, want: []ops.Resource{ops.ResBookFiles},
		seed: func(t *testing.T) *t6g4LiveEnv {
			p, store, root := newCaptureFixture(t, map[string]string{"a.m4b": "alpha-bytes", "sub/b.m4b": "bravo-bytes"})
			return &t6g4LiveEnv{
				p: p,
				observe: func() string {
					rows, err := store.ScanFileEventsBySeq(0, 100)
					if err != nil {
						t.Fatal(err)
					}
					var lines []string
					for _, r := range rows {
						rel, _ := filepath.Rel(root, r.Event.Path)
						lines = append(lines, fmt.Sprintf("%s %s size=%d", rel, r.Event.Kind, r.Event.Digest.SizeBytes))
					}
					slices.Sort(lines)
					return strings.Join(lines, "\n")
				},
				headParams: fmt.Sprintf(`{"roots":[%q],"apply":true}`, root),
				params:     fmt.Sprintf(`{"roots":[%q]}`, root),
			}
		},
	})

	// file-provenance-export: three ledger events; the run appends them to a JSONL
	// file (a write outside the writer, declared as files) and advances the cursor
	// (a writer call, recorded as book_files).
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: fileProvenanceExportKind, want: []ops.Resource{ops.ResBookFiles}, unrecorded: []ops.Resource{ops.ResFiles},
		seed: func(t *testing.T) *t6g4LiveEnv {
			p, store, out := newExportFixture(t)
			seedEvents(t, store, 3)
			return &t6g4LiveEnv{
				p: p,
				observe: func() string {
					cur, err := store.GetFileProvExportCursor()
					if err != nil {
						t.Fatal(err)
					}
					raw, err := os.ReadFile(out)
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					var seqs []string
					for _, ln := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
						if len(ln) == 0 {
							continue
						}
						var l fileProvExportLine
						if err := json.Unmarshal(ln, &l); err != nil {
							t.Fatal(err)
						}
						seqs = append(seqs, fmt.Sprintf("%d:%s", l.Seq, l.Event.Kind))
					}
					return fmt.Sprintf("cursor=%d lines=%v", cur, seqs)
				},
				headParams: fmt.Sprintf(`{"path":%q,"apply":true}`, out),
				params:     fmt.Sprintf(`{"path":%q}`, out),
			}
		},
	})

	// fs-regroup-xml: three chapter-fragment books and an iTunes id on the last
	// one. The apply merges them onto one survivor (book_files rows created and
	// moved, books modified), repoints the id (external_ids) and journals every
	// change (operations).
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: fsRegroupKind, headParams: `{"dry_run":false}`, params: `{}`,
		want: []ops.Resource{ops.ResBooks, ops.ResBookFiles, ops.ResExternalIDs, ops.ResOperations},
		seed: func(t *testing.T) *t6g4LiveEnv {
			s := regroupStore(t)
			base := fsFragBase(t)
			ids := seedFragments(t, s, base, "Cage of Souls", 3)
			if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "PID-X", BookID: ids[2]}); err != nil {
				t.Fatal(err)
			}
			return &t6g4LiveEnv{p: New(fakeDeps{store: s}), observe: func() string {
				var lines []string
				for _, id := range ids {
					b, err := s.GetBookByID(id)
					if err != nil || b == nil {
						t.Fatalf("book %s is gone: %v", id, err)
					}
					files, err := s.GetBookFiles(id)
					if err != nil {
						t.Fatal(err)
					}
					var rows []string
					for _, f := range files {
						rows = append(rows, fmt.Sprintf("%s#%d", strings.TrimPrefix(f.FilePath, base), f.TrackNumber))
					}
					slices.Sort(rows)
					lines = append(lines, fmt.Sprintf("deleted=%v title=%q path=%q rows=%v", b.IsSoftDeleted(), b.Title, strings.TrimPrefix(b.FilePath, base), rows))
				}
				slices.Sort(lines)
				owner, _ := s.GetBookByExternalID("itunes", "PID-X")
				who := "none"
				if b, err := s.GetBookByID(owner); err == nil && b != nil {
					who = fmt.Sprintf("title=%q deleted=%v", b.Title, b.IsSoftDeleted())
				}
				return strings.Join(lines, "\n") + "\nPID-X -> " + who
			}}
		},
	})

	// extract-wav-clips: a one-file book whose source exists on disk. A stand-in
	// ffmpeg earlier on PATH writes the clip, so the run hashes the source and
	// persists the hash on the row that has none (book_files).
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: extractWAVClipsKind, headParams: `{}`, params: `{}`,
		want: []ops.Resource{ops.ResBookFiles},
		seed: func(t *testing.T) *t6g4LiveEnv {
			t.Setenv(serverdecode.EnvVar, "true")
			bin := t.TempDir()
			script := "#!/bin/sh\nfor a; do last=\"$a\"; done\nprintf 'RIFFstand-in-clip' > \"$last\"\n"
			if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			s := t6g4MemStore(t)
			root := t.TempDir()
			src := filepath.Join(root, "Gotham Knights", "01.m4b")
			if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(src, []byte("source-audio-bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			b, err := s.CreateBook(&database.Book{Title: "Gotham Knights", FilePath: filepath.Dir(src)})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: src}); err != nil {
				t.Fatal(err)
			}
			p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: s}, root: root}}
			return &t6g4LiveEnv{p: p, observe: func() string {
				files, err := s.GetBookFiles(b.ID)
				if err != nil {
					t.Fatal(err)
				}
				var rows []string
				for _, f := range files {
					rows = append(rows, fmt.Sprintf("%s hash=%q", strings.TrimPrefix(f.FilePath, root), f.FileHash))
				}
				entries, _ := os.ReadDir(wavCacheDir(root))
				return fmt.Sprintf("rows=%v cache-entries=%d", rows, len(entries))
			}}
		},
	})

	// transcribe-book-intros: the source file is absent, so the run reaches its
	// first writes (the book's status row, the stats key, the result journal)
	// without ffmpeg or Whisper. The book_files half of the declaration (a hash
	// persisted after a real clip extraction) needs an ffmpeg stand-in and the
	// transcribeBatchFn seam, and is not exercised here.
	t6g4LiveCases = append(t6g4LiveCases, t6g4LiveCase{
		kind: introTranscribeKind, headParams: `{"dry_run":false}`, params: `{}`,
		want:    []ops.Resource{ops.ResBooks},
		partial: "missing source file: the book_files hash write after a clip extraction is not reached",
		seed: func(t *testing.T) *t6g4LiveEnv {
			orig := config.AppConfig.WhisperClipCacheDir
			config.AppConfig.WhisperClipCacheDir = ""
			t.Cleanup(func() { config.AppConfig.WhisperClipCacheDir = orig })
			t.Setenv(serverdecode.EnvVar, "1")
			s := t6g4MemStore(t)
			root := t.TempDir()
			for _, title := range []string{"Hush", "Hush Returns"} {
				if _, err := s.CreateBook(&database.Book{Title: title, FilePath: filepath.Join(root, "missing", title+".mp3")}); err != nil {
					t.Fatal(err)
				}
			}
			p := New(fakeDeps{store: s, root: root})
			return &t6g4LiveEnv{p: p, observe: func() string { return t6g4ObserveBooks(t, s) }}
		},
	})
}
