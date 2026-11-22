// file: internal/books/cutover/m4/corpus_test.go
// version: 1.0.0
// guid: 6d2e8b05-a4c1-4739-9f60-e83b15d7c4a2
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The corpus checks run M4 over the R2 corpus's two stores (F2-12's stage 1,
// which R0 wrote through its API, and F2-13's overlay, which adds the rows R0's
// API cannot write) and compare the result with the source store and with the
// manifest, whose M4 cases say what each hand-built row must become.

// corpusAnswer is what M4 does to one corpus store, in the numbers
// answers.json holds.
type corpusAnswer struct {
	RunsInSource    int            `json:"runs_in_source"`
	RunsImported    int            `json:"runs_imported"`
	RowsQuarantined int            `json:"rows_quarantined"`
	QuarantinedRuns []string       `json:"quarantined_runs"`
	ByKind          map[string]int `json:"imported_by_kind"`
	ByState         map[string]int `json:"imported_by_state"`
	Totals          map[string]int `json:"totals"`
	VerifyRead      int            `json:"verify_read"`
	VerifyWritten   int            `json:"verify_written"`
}

// preRun is what the source store held for one run before the boot.
type preRun struct {
	def, state, stop, wait, why string
	params                      json.RawMessage
	evt, intent, item           int
	log, log2, err2             int
	rows                        int
	marks                       map[string]bool
}

// preCensus reads every run-owned row of the source store.
func preCensus(ctx context.Context, t testing.TB, lr storage.LegacyRange) map[string]*preRun {
	t.Helper()
	runs := map[string]*preRun{}
	get := func(id string) *preRun {
		if runs[id] == nil {
			runs[id] = &preRun{marks: map[string]bool{}}
		}
		return runs[id]
	}
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{Start: []byte("op"), End: []byte("op\xff")}) {
		if err != nil {
			t.Fatal(err)
		}
		k := string(kv.Key)
		switch {
		case strings.HasPrefix(k, "opv3:run:"):
			var row struct {
				DefID          string          `json:"def_id"`
				State          string          `json:"state"`
				StopReason     string          `json:"stop_reason"`
				WaitReason     string          `json:"wait_reason"`
				InterruptedWhy string          `json:"interrupted_why"`
				Params         json.RawMessage `json:"params"`
			}
			if json.Unmarshal(kv.Value, &row) != nil {
				get(strings.TrimPrefix(k, "opv3:run:")) // a corrupt run: counted, never compared
				continue
			}
			p := get(strings.TrimPrefix(k, "opv3:run:"))
			p.def, p.state, p.stop, p.wait, p.why, p.params = row.DefID, row.State, row.StopReason, row.WaitReason, row.InterruptedWhy, row.Params
		case strings.HasPrefix(k, "opv3:evt:"):
			get(idOf(k, "opv3:evt:")).evt++
		case strings.HasPrefix(k, "opv3:intent:"):
			get(idOf(k, "opv3:intent:")).intent++
		case strings.HasPrefix(k, "opv3:item:"):
			get(idOf(k, "opv3:item:")).item++
		case strings.HasPrefix(k, "opv3:log:"):
			get(idOf(k, "opv3:log:")).log++
		case strings.HasPrefix(k, "opv2:log:"):
			get(idOf(k, "opv2:log:")).log2++
		case strings.HasPrefix(k, "opv2:err:"):
			get(idOf(k, "opv2:err:")).err2++
		case strings.HasPrefix(k, "opv2:row:"):
			get(idOf(k, "opv2:row:")).rows++
		case strings.HasPrefix(k, "opv3:rowmark:"):
			id := idOf(k, "opv3:rowmark:")
			get(id).marks[strings.TrimPrefix(k, "opv3:rowmark:"+id+":")] = true
		case strings.HasPrefix(k, "opv3:mark:"):
			id := idOf(k, "opv3:mark:")
			_, name, _ := strings.Cut(strings.TrimPrefix(k, "opv3:mark:"+id+":"), "\x00")
			get(id).marks[name] = true
		}
	}
	return runs
}

// defToken finds the def-ID-shaped words in a row's key and value.
var defToken = regexp.MustCompile(`[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)+`)

func idOf(key, prefix string) string {
	id, _, _ := strings.Cut(strings.TrimPrefix(key, prefix), ":")
	return id
}

type manifestCase struct {
	Case        string            `json:"case"`
	Step        string            `json:"step"`
	Keys        []string          `json:"keys"`
	CorruptKeys []string          `json:"corrupt_keys"`
	Outcome     string            `json:"outcome"`
	ImportedAs  map[string]string `json:"imported_as"`
}

func overlayCases(t testing.TB) []manifestCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(corpusRel), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Overlay []manifestCase `json:"overlay"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var out []manifestCase
	for _, c := range m.Overlay {
		if c.Step == "M4" {
			out = append(out, c)
		}
	}
	return out
}

// expectedKind is the kind the rename map gives a source run: rule 4's split
// for library.scan, the generated map's answer for every other def ID.
func expectedKind(t *testing.T, p *preRun) tasks.Kind {
	t.Helper()
	tg, err := kindrename.Resolve(p.def)
	if err != nil {
		t.Fatalf("def %q: %v", p.def, err)
	}
	rt, err := tg.ForRun(p.params)
	if err != nil {
		t.Fatalf("def %q: %v", p.def, err)
	}
	return rt.Kind
}

// TestCutover_TasksFixtureFromR0: M4 over each corpus store. After it every
// source row of the step's families is gone (or quarantined, for the one
// hand-built corrupt run), every run is present under its HEAD ID with the
// kind the map gives its def ID and the state 5T.8.3's table gives its stored
// state, and every count of a run's trail, intents, items, log lines, plan rows
// and marks equals the source's. The manifest says what each hand-built row
// must become and the overlay is held to it.
func TestCutover_TasksFixtureFromR0(t *testing.T) {
	for _, corpus := range []string{"stage1", "overlay"} {
		t.Run(corpus, func(t *testing.T) {
			var pre map[string]*preRun
			r := newRig(t, withCorpus(corpus))
			r.chunkItems = 7
			r.mutate = func(ctx context.Context, t testing.TB, lr storage.LegacyRange) { pre = preCensus(ctx, t, lr) }
			r.runObserve(t, func(ctx context.Context, s *snap) {
				if len(pre) == 0 {
					t.Fatal("the corpus holds no run: the test would compare nothing with nothing")
				}
				if len(s.Left) != 0 {
					t.Errorf("%d source rows left: %v", len(s.Left), keysOf(s.Left))
				}
				if len(s.Verify.Failures) != 0 {
					t.Errorf("verify failures: %v", s.Verify.Failures)
				}
				corrupt := map[string]bool{}
				if corpus == "overlay" {
					for _, c := range overlayCases(t) {
						for _, k := range c.CorruptKeys {
							corrupt[idOf(k, "opv3:run:")] = true
						}
					}
				}
				quarantinedRuns := map[string]bool{}
				for _, q := range s.Quarantine {
					// opv3:<family>:<run>... and opv2:<family>:<run>...
					parts := strings.SplitN(q.Key, ":", 4)
					if len(parts) < 3 || (parts[0] != "opv3" && parts[0] != "opv2") {
						t.Errorf("quarantined row %s is in no run-owned family", q.Key)
						continue
					}
					quarantinedRuns[parts[2]] = true
				}
				for id := range quarantinedRuns {
					if !corrupt[id] {
						t.Errorf("run %s is quarantined, but the manifest names no corrupt run with that ID\n%+v", id, s.Quarantine)
					}
				}

				ans := corpusAnswer{RunsInSource: len(pre), ByKind: map[string]int{}, ByState: map[string]int{}, Totals: map[string]int{},
					RowsQuarantined: len(s.Quarantine), VerifyRead: int(s.Verify.Read), VerifyWritten: int(s.Verify.Written), QuarantinedRuns: []string{}}
				for id := range quarantinedRuns {
					ans.QuarantinedRuns = append(ans.QuarantinedRuns, id)
				}
				slices.Sort(ans.QuarantinedRuns)
				imported := 0
				for id, p := range pre {
					if p.def == "" { // the corrupt run's record does not decode
						if !corrupt[id] {
							t.Errorf("run %s has no decodable record and the manifest does not call it corrupt", id)
						}
						if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(id)); err == nil {
							t.Errorf("the corrupt run %s was imported", id)
						}
						continue
					}
					rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(id))
					if err != nil {
						t.Errorf("run %s (%s): %v", id, p.def, err)
						continue
					}
					imported++
					ans.ByKind[string(rec.Kind)]++
					ans.ByState[string(rec.State)]++
					ans.Totals["events"] += p.evt
					ans.Totals["intents"] += p.intent
					ans.Totals["items"] += p.item
					ans.Totals["log_lines"] += p.log + p.log2 + p.err2
					ans.Totals["plan_rows"] += p.rows
					ans.Totals["marks"] += len(p.marks)
					if want := expectedKind(t, p); rec.Kind != want {
						t.Errorf("run %s: kind %q, want %q for %s", id, rec.Kind, want, p.def)
					}
					count := func(pk tasks.PageKind) int {
						return len(collect(t, func(y func(tasks.PageEntry, error) bool) {
							s.Stores.Runs.Pages(ctx, tasks.RunID(id), tasks.PageQuery{Page: pk})(y)
						}))
					}
					for pk, want := range map[tasks.PageKind]int{
						tasks.PageEvents: p.evt, tasks.PageIntents: p.intent, tasks.PageItems: p.item, tasks.PageLog: p.log + p.log2 + p.err2,
					} {
						if got := count(pk); got != want {
							t.Errorf("run %s: %d %v pages, source held %d", id, got, pk, want)
						}
					}
					if rows := collect(t, func(y func(tasks.Row, error) bool) { s.Stores.Runs.Rows(ctx, tasks.RunID(id))(y) }); len(rows) != p.rows {
						t.Errorf("run %s: %d plan rows, source held %d", id, len(rows), p.rows)
					}
					if marks := collect(t, func(y func(string, error) bool) { s.Stores.Runs.Marks(ctx, tasks.RunID(id))(y) }); len(marks) != len(p.marks) {
						t.Errorf("run %s: %d marks, source held %d", id, len(marks), len(p.marks))
					}
					// A state HEAD stored as a terminal one, or as awaiting_decision,
					// is imported as itself.
					switch p.state {
					case "succeeded", "failed", "timed_out", "canceled", "awaiting_decision":
						if string(rec.State) != p.state {
							t.Errorf("run %s: state %q, stored as %q", id, rec.State, p.state)
						}
					}
				}
				// Whole-store scan: no row left in books' legacy range quotes a HEAD
				// def ID the table renames.
				hits := map[string]int{}
				for kv, err := range s.legacy.Scan(ctx, storage.ScanOptions{}) {
					if err != nil {
						t.Fatal(err)
					}
					for _, id := range defToken.FindAllString(string(kv.Key)+" "+string(kv.Value), -1) {
						if tg, ok := kindrename.Lookup(id); ok && tg.Kind != "" && string(tg.Kind) != id {
							fam, _, _ := strings.Cut(string(kv.Key), ":")
							hits[fam+" "+id]++
						}
					}
				}
				for fam := range hits {
					// The feed is the activity step's, which has its own corpus test.
					// role: and apikey: rows hold auth permission names, which share
					// the "library.organize" spelling with a def ID (auth.PermLibraryOrganize)
					// and are not task references; the two families are named, not
					// the IDs.
					if !strings.HasPrefix(fam, "act ") && !strings.HasPrefix(fam, "role ") && !strings.HasPrefix(fam, "apikey ") {
						t.Errorf("STOP: a row outside the feed still quotes a renamed HEAD def ID (%s): %d rows", fam, hits[fam])
					}
				}
				ans.RunsImported = imported
				checkAnswers(t, corpus, ans)
				t.Logf("%s: %d runs in the source, %d imported, %d rows quarantined", corpus, len(pre), imported, len(s.Quarantine))
				if imported < 10 {
					t.Errorf("only %d runs imported; the corpus holds more", imported)
				}

				// The paused executor R0 left (stage 1's runs.paused) and the stand-down,
				// bucket and stamp the overlay hand-built.
				if p, ok, err := s.Stores.Pause.Get(ctx); err != nil || !ok || p.At.IsZero() {
					t.Errorf("the executor was left paused in the source: pause = %+v, %v, %v", p, ok, err)
				}
				if corpus != "overlay" {
					return
				}
				holds := collect(t, func(y func(tasks.StandDown, error) bool) { s.Stores.StandDown.List(ctx)(y) })
				if len(holds) != 1 || holds[0].Key != scanKey || holds[0].Holder == "" {
					t.Errorf("stand-down holds = %+v, want the one marker for the scan kind", holds)
				}
				buckets := collect(t, func(y func(tasks.DebounceBucket, error) bool) { s.Stores.Runs.DebounceBuckets(ctx)(y) })
				if len(buckets) != 1 || buckets[0].Kind != "books.dedup.check-book" || len(buckets[0].Subjects) != 1 || buckets[0].Subjects[0].ID != "01M72KQYE310SSXK8P6KZHNDG4" {
					t.Errorf("debounce buckets = %+v, want the one pending dedup.check-book start", buckets)
				}
				if _, ok := stampOf(ctx, t, s, "books", "01M72KQYE310SSXK8P6KZHNDG4", "books.dedup.check-book"); !ok {
					t.Error("the completion stamp of dedup.check-book was not imported for its book")
				}

				// The manifest's own account of the hand-built overlay rows.
				for _, c := range overlayCases(t) {
					for _, k := range c.Keys {
						if _, ok := s.Left[k]; ok {
							t.Errorf("%s: %s is still in the source", c.Case, k)
						}
					}
					if c.Outcome == "quarantined" {
						got := map[string]bool{}
						for _, q := range s.Quarantine {
							got[q.Key] = true
						}
						for _, k := range c.CorruptKeys {
							if !got[k] {
								t.Errorf("%s: the corrupt row %s is not in the quarantine", c.Case, k)
							}
						}
						continue
					}
					if want, ok := c.ImportedAs["state"]; ok {
						var id string
						for _, k := range c.Keys {
							if after, ok0 := strings.CutPrefix(k, "opv3:run:"); ok0 {
								id = after
							}
						}
						rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(id))
						if err != nil {
							t.Errorf("%s: %v", c.Case, err)
							continue
						}
						if string(rec.State) != want {
							t.Errorf("%s: imported as %q, the manifest says %q", c.Case, rec.State, want)
						}
						if why := c.ImportedAs["reason"]; why != "" && rec.Reason != why {
							t.Errorf("%s: reason %q, the manifest says %q", c.Case, rec.Reason, why)
						}
					}
				}
			})
		})
	}
}

// TestCutover_RepairsRowsAndMarksImported: the two hand-built repairs runs of
// the overlay. The plan awaiting its approver comes over as a Preview run of
// the fixer's own kind, in awaiting_decision, with both of its plan rows; the
// interrupted live apply comes over as a Live run of the same kind, interrupted
// (so its ResumePolicy decides at the next boot), naming its plan run and
// carrying the row marker that stops a resume applying that row twice.
func TestCutover_RepairsRowsAndMarksImported(t *testing.T) {
	const (
		planID  = "01M731PC45NGKDVXDXWY0TRG4V"
		applyID = "01M731PC4KT6TMXV8QM6VB69YH"
	)
	var pre map[string]*preRun
	r := newRig(t, withCorpus("overlay"))
	r.mutate = func(ctx context.Context, t testing.TB, lr storage.LegacyRange) { pre = preCensus(ctx, t, lr) }
	r.runObserve(t, func(ctx context.Context, s *snap) {
		plan, apply := pre[planID], pre[applyID]
		if plan == nil || apply == nil {
			t.Fatalf("the overlay no longer holds the two repairs runs: plan %v, apply %v", plan != nil, apply != nil)
		}
		prec, err := s.Stores.Runs.Get(ctx, tasks.RunID(planID))
		if err != nil {
			t.Fatal(err)
		}
		arec, err := s.Stores.Runs.Get(ctx, tasks.RunID(applyID))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(prec.Kind), kindrename.RepairsKindPrefix) || prec.Kind != arec.Kind {
			t.Errorf("kinds %q and %q: want one books.repairs.<fixer> kind for both runs", prec.Kind, arec.Kind)
		}
		if prec.Mode != tasks.ModePreview || prec.State != tasks.StateAwaitingDecision {
			t.Errorf("plan run: mode %q state %q, want preview and awaiting_decision", prec.Mode, prec.State)
		}
		if arec.Mode != tasks.ModeLive || arec.State != tasks.StateInterrupted {
			t.Errorf("apply run: mode %q state %q, want live and interrupted", arec.Mode, arec.State)
		}
		if arec.PlanRef != tasks.RunID(planID) {
			t.Errorf("apply run PlanRef = %q, want the plan run %q", arec.PlanRef, planID)
		}
		rows := collect(t, func(y func(tasks.Row, error) bool) { s.Stores.Runs.Rows(ctx, tasks.RunID(planID))(y) })
		if len(rows) != 2 {
			t.Errorf("plan run holds %d rows, want 2", len(rows))
		}
		if rows := collect(t, func(y func(tasks.Row, error) bool) { s.Stores.Runs.Rows(ctx, tasks.RunID(applyID))(y) }); len(rows) != 0 {
			t.Errorf("apply run holds %d plan rows; its plan's rows belong to the plan run", len(rows))
		}
		marks := collect(t, func(y func(string, error) bool) { s.Stores.Runs.Marks(ctx, tasks.RunID(applyID))(y) })
		if !apply.marks["row-hush"] || len(marks) != len(apply.marks) || len(marks) != 3 {
			t.Errorf("apply run marks = %v (source %v), want its row marker and its two touched marks", marks, apply.marks)
		}
	})
}
