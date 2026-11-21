// file: internal/books/cutover/m4/stamps_test.go
// version: 1.0.0
// guid: e17a94c3-5b60-4d28-8f0a-c3d62e91b7a4
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

func stampOf(ctx context.Context, t *testing.T, s *snap, kind, id string, k tasks.Kind) (stampedAt string, ok bool) {
	t.Helper()
	at, ok, err := s.Stores.Runs.Stamped(ctx, entity.Ref{Module: "books", Kind: kind, ID: id}, k)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return "", false
	}
	return at.UTC().Format("2006-01-02T15:04:05Z"), true
}

// TestCutover_StampsImportedWhileCurrent: a completion whose revision has not
// fallen behind its subject's dependency revision becomes a Requires stamp for
// the renamed kind (book-level on books/books, per-file on books/book_files);
// a stale one, one of a retired kind and every revision row are dropped; two
// completions that name one stamp (an old op ID and its alias) import it once.
func TestCutover_StampsImportedWhileCurrent(t *testing.T) {
	raw := map[string]string{
		"op:deprev:book:year-one":                                       revJSON(2),
		"op:completion:book:year-one:dedup.book-scan":                   revJSON(2),
		"op:completion:book:year-one:metafetch.asin-backfill":           revJSON(1), // stale
		"op:completion:book:year-one:dedup.book-scan:file-9":            revJSON(3), // per file, ahead of the revision
		"op:completion:book:year-one:operations.backfill-legacy-status": revJSON(2), // retired
		"op:completion:book:year-one:itunes.heal":                       revJSON(2),
		"op:completion:book:year-one:maintenance.itunes-heal":           revJSON(2), // alias of the line above

		// Never bumped: revision 0, and every completion is current.
		"op:completion:book:killing-joke:dedup.book-scan": revJSON(0),

		// A revision no completion needs.
		"op:deprev:book:the-cult": revJSON(5),
	}
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 || len(s.Quarantine) != 0 {
			t.Errorf("left %v, quarantined %+v", keysOf(s.Left), s.Quarantine)
		}
		// The report counts what was imported: four stamps, the alias pair once.
		if s.Verify.Written != 4 {
			t.Errorf("verify report wrote %d, want 4 stamps: %+v", s.Verify.Written, s.Verify)
		}
		want := "2026-11-06T04:00:00Z"
		for _, c := range []struct {
			kind, id string
			k        tasks.Kind
			ok       bool
		}{
			{"books", "year-one", "books.dedup.book-scan", true},
			{"book_files", "file-9", "books.dedup.book-scan", true},
			{"books", "year-one", "books.itunes.heal", true},
			{"books", "killing-joke", "books.dedup.book-scan", true},
			{"books", "year-one", "books.metafetch.asin-backfill", false},
			{"book_files", "year-one", "books.dedup.book-scan", false},
			{"books", "year-one", "books.retired.operations.backfill-legacy-status", false},
		} {
			at, ok := stampOf(ctx, t, s, c.kind, c.id, c.k)
			if ok != c.ok || (ok && at != want) {
				t.Errorf("stamp %s/%s %s = %q, %v; want present=%v at %s", c.kind, c.id, c.k, at, ok, c.ok, want)
			}
		}
	})
}

// TestCutover_StampsRefuseWhatTheyCannotJudge: a completion that does not
// decode, one of an operation that names no kind, and a subject whose revision
// row does not decode (so no completion of it can be judged current) are set
// aside by themselves, never imported and never dropped.
func TestCutover_StampsRefuseWhatTheyCannotJudge(t *testing.T) {
	raw := map[string]string{
		"op:completion:book:year-one:dedup.book-scan":    "not json",
		"op:completion:book:year-one:itunes.heal":        revJSON(1),
		"op:completion:book:year-one:Not A Kind!":        revJSON(1),
		"op:completion:book:arkham:dedup.book-scan":      revJSON(1),
		"op:deprev:book:arkham":                          "garbage",
		"op:completion:book:blackgate:dedup.book-scan":   revJSON(1),
		"op:completion:book":                             revJSON(1), // no subject at all
		"op:completion:book:riddler:dedup.book-scan:f:x": revJSON(1), // one part too many
	}
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 {
			t.Errorf("source rows left: %v", keysOf(s.Left))
		}
		quar := map[string]bool{}
		for _, q := range s.Quarantine {
			quar[q.Key] = true
		}
		for _, k := range []string{
			"op:completion:book:year-one:dedup.book-scan", "op:completion:book:year-one:Not A Kind!",
			"op:completion:book:arkham:dedup.book-scan", "op:deprev:book:arkham", "op:completion:book",
			"op:completion:book:riddler:dedup.book-scan:f:x",
		} {
			if !quar[k] {
				t.Errorf("%q was not quarantined (quarantined: %v)", k, quar)
			}
		}
		if len(quar) != 6 {
			t.Errorf("quarantined %d rows, want 6: %v", len(quar), quar)
		}
		for id, k := range map[string]tasks.Kind{"year-one": "books.itunes.heal", "blackgate": "books.dedup.book-scan"} {
			if _, ok := stampOf(ctx, t, s, "books", id, k); !ok {
				t.Errorf("the good completion of %s was not imported", id)
			}
		}
		if _, ok := stampOf(ctx, t, s, "books", "arkham", "books.dedup.book-scan"); ok {
			t.Error("a completion was imported although its subject's revision could not be read")
		}
	})
}

// TestM4_StampShardsDisjoint: many subjects, each with completions at, behind
// and ahead of its dependency revision, imported by parallel workers. The
// revision row sorts after its subject's completions but another shard can be
// handed it while the subject's driver is still running; it must leave the
// revision for the driver, or the driver reads revision 0 and stamps completions
// that are stale. Run with a worker per CPU and the subjects interleaved, the
// outcome is the same every time: only current completions are stamped, once.
func TestM4_StampShardsDisjoint(t *testing.T) {
	raw := map[string]string{}
	const subjects = 60
	for i := range subjects {
		id := fmt.Sprintf("gotham-%03d", i)
		raw["op:deprev:book:"+id] = revJSON(2)
		raw["op:completion:book:"+id+":dedup.book-scan"] = revJSON(2)
		raw["op:completion:book:"+id+":metafetch.asin-backfill"] = revJSON(1) // stale
		raw["op:completion:book:"+id+":itunes.heal"] = revJSON(3)
		raw["op:completion:book:"+id+":dedup.book-scan:file-"+id] = revJSON(2)
	}
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 || len(s.Quarantine) != 0 {
			t.Errorf("left %d rows, quarantined %d", len(s.Left), len(s.Quarantine))
		}
		if want := int64(3 * subjects); s.Verify.Written != want {
			t.Errorf("wrote %d stamps, want %d", s.Verify.Written, want)
		}
		for i := range subjects {
			id := fmt.Sprintf("gotham-%03d", i)
			if _, ok := stampOf(ctx, t, s, "books", id, "books.metafetch.asin-backfill"); ok {
				t.Fatalf("%s: a stale completion was stamped", id)
			}
			for _, c := range []struct {
				kind, id string
				k        tasks.Kind
			}{{"books", id, "books.dedup.book-scan"}, {"books", id, "books.itunes.heal"}, {"book_files", "file-" + id, "books.dedup.book-scan"}} {
				if _, ok := stampOf(ctx, t, s, c.kind, c.id, c.k); !ok {
					t.Fatalf("%s: stamp %s/%s %s missing", id, c.kind, c.id, c.k)
				}
			}
		}
	})
}

// strayMode is a runner shape the stray-key tests run under. sequential_chunk_64
// is the one that failed every time before the fix: a stray key that sorts
// lowest is quarantined in the same chunk as the rows that defer to it, the
// step's live reads cannot see a quarantine the chunk has only staged, and so
// nothing drove the group and Verify failed the boot.
type strayMode struct {
	name  string
	chunk int
	seq   bool
}

var strayModes = []strayMode{
	{"sequential_chunk_1", 1, true},
	{"sequential_chunk_64", 64, true},
	{"parallel_chunk_1", 1, false},
}

func (m strayMode) rig(t *testing.T, raw map[string]string) *rig {
	r := newRig(t, withRaw(raw), func(r *rig) { r.chunkItems = m.chunk })
	if m.seq {
		r.parallelism = func(dm *datamigrate.DataMigration) { dm.Parallelism = fixedOne() }
	}
	return r
}

// quarantineReasons checks that the quarantine holds exactly want's keys, each
// with a reason containing want's text.
func quarantineReasons(t *testing.T, s *snap, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, q := range s.Quarantine {
		got[q.Key] = q.Reason
	}
	for k, w := range want {
		if !strings.Contains(got[k], w) {
			t.Errorf("%s: reason %q, want one saying %q", k, got[k], w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s was quarantined unexpectedly (%q)", k, got[k])
		}
	}
}

// TestCutover_StampsStrayKeySetsTheSubjectAside: a row under a subject's prefix
// whose key does not decode as a completion (five parts, or no operation) is
// the subject's: the subject's lowest row drives whatever its shape, and the
// stray key sets the whole subject aside, its revision row with it, wherever
// the stray sorts and however the runner cuts chunks or shards. Another subject
// imports as usual.
func TestCutover_StampsStrayKeySetsTheSubjectAside(t *testing.T) {
	for _, tc := range []struct{ name, stray string }{
		{"stray_lowest", "op:completion:book:year-one:a:b:c"},
		{"stray_highest", "op:completion:book:year-one:zzz:b:c"},
		{"stray_no_operation", "op:completion:book:year-one:"},
	} {
		for _, mode := range strayModes {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				raw := map[string]string{
					tc.stray: revJSON(2),
					"op:completion:book:year-one:dedup.book-scan":     revJSON(2),
					"op:completion:book:year-one:itunes.heal":         revJSON(2),
					"op:deprev:book:year-one":                         revJSON(2),
					"op:completion:book:killing-joke:dedup.book-scan": revJSON(0),
				}
				r := mode.rig(t, raw)
				r.runObserve(t, func(ctx context.Context, s *snap) {
					whole := "set aside whole"
					quarantineReasons(t, s, map[string]string{
						tc.stray: "key or value does not decode",
						"op:completion:book:year-one:dedup.book-scan": whole,
						"op:completion:book:year-one:itunes.heal":     whole,
						"op:deprev:book:year-one":                     whole,
					})
					if len(s.Left) != 0 {
						t.Errorf("left %v", keysOf(s.Left))
					}
					for _, k := range []tasks.Kind{"books.dedup.book-scan", "books.itunes.heal"} {
						if _, ok := stampOf(ctx, t, s, "books", "year-one", k); ok {
							t.Errorf("the set-aside subject was stamped for %s", k)
						}
					}
					if _, ok := stampOf(ctx, t, s, "books", "killing-joke", "books.dedup.book-scan"); !ok {
						t.Error("the other subject's completion was not imported")
					}
					if s.Verify.Written != 1 || s.Verify.Quarantined != 4 {
						t.Errorf("report: %+v", s.Verify)
					}
				})
			})
		}
	}
}
