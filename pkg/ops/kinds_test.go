// file: pkg/ops/kinds_test.go
// version: 1.0.1
// guid: d3536389-9d87-4f21-b694-6ce022e230a8
// last-edited: 2026-10-13

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// titleWriter is a minimal Writer over an in-memory title map.
type titleWriter struct {
	mu     sync.Mutex
	titles map[string]string
}

func (w *titleWriter) ModifyBook(_ context.Context, id string, fn func(*database.Book) error) (Changed, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := &database.Book{ID: id, Title: w.titles[id]}
	if err := fn(b); err != nil {
		return Changed{}, err
	}
	w.titles[id] = b.Title
	return Changed{Fields: []string{"title"}}, nil
}
func (w *titleWriter) ModifyBookFile(context.Context, string, string, func(*database.BookFile) error) (Changed, error) {
	return Changed{}, errors.New("unused")
}
func (w *titleWriter) RepointBookFile(context.Context, string, string, undo.BookFileLocation, undo.BookFileLocation) error {
	return errors.New("unused")
}
func (w *titleWriter) ModifyBookAuthors(context.Context, string, func([]database.BookAuthor) ([]database.BookAuthor, error)) (Changed, error) {
	return Changed{}, errors.New("unused")
}
func (w *titleWriter) Effect(context.Context, EffectSpec, func() ([]HistoryRow, error)) error {
	return errors.New("unused")
}

// ordinalFixer is examples.md Example 3 against an in-memory title map:
// rewrite a leading "l " ordinal to "1 ".
func ordinalFixer(titles map[string]string, guard Guard) Definition {
	evaluate := func(_ *ItemRC, _ NoParams, b database.Book) (Row, error) {
		row := Row{ID: b.ID, Subjects: Books(b.ID), Title: b.Title, Risk: RiskLow,
			Current: map[string]string{"title": b.Title}, Inputs: map[string]string{"title": b.Title}}
		fixed, ok := strings.CutPrefix(b.Title, "l ")
		if !ok {
			row.Hold = "the title no longer uses a letter-l ordinal"
			return row, nil
		}
		row.Proposed = map[string]string{"title": "1 " + fixed}
		row.Reason = "the ordinal is the letter l"
		return row, nil
	}
	var guards []Guard
	if guard != nil {
		guards = []Guard{guard}
	}
	return Fixer("maintenance.normalize-letter-l-ordinals", FixerSpec[NoParams, database.Book]{
		Common: Common{Title: "Letter-l ordinals", Effects: Writes(ResBooks, ResFieldStates), Permission: auth.PermSettingsManage},
		Candidates: func(*RC, NoParams) (Source[database.Book], error) {
			var out []database.Book
			for id, title := range titles {
				out = append(out, database.Book{ID: id, Title: title})
			}
			return Source[database.Book]{Items: out, Key: func(b database.Book) string { return b.ID }, Order: Snapshot}, nil
		},
		Load: func(_ *RC, _ NoParams, id string) (database.Book, bool, error) {
			t, ok := titles[id]
			return database.Book{ID: id, Title: t}, ok, nil
		},
		Evaluate: evaluate,
		Apply: func(rc *ItemRC, w Writer, _ NoParams, row Row) error {
			_, err := w.ModifyBook(rc.Context(), row.ID, func(b *database.Book) error {
				if b.Title != row.Current["title"] {
					return ErrChangedSincePlan
				}
				b.Title = row.Proposed["title"]
				return nil
			})
			return err
		},
		Guards: guards,
	})
}

func TestFixerPlanThenApply(t *testing.T) {
	titles := map[string]string{"b1": "l Volume", "b2": "Plain", "b3": "l Book", "b4": "l Gone"}
	def := ordinalFixer(titles, func(r Row) string {
		if r.ID == "b3" {
			return "owner_manual"
		}
		return ""
	})
	// Plan (Preview): every candidate evaluated, held rows skipped.
	job, err := Compile(def).OpenBatch(newTestRC(nil, Preview), nil)
	if err != nil {
		t.Fatal(err)
	}
	if job.Resume != ResumeFromZero {
		t.Fatalf("a plan restarts from zero, got %v", job.Resume)
	}
	skips, _ := runSet(t, newTestRC(nil, Preview), job.Items)
	var plan FixerPlan
	raw, err := job.Finish(newTestRC(nil, Preview), Summary{})
	if err != nil || json.Unmarshal(raw, &plan) != nil {
		t.Fatalf("plan result %s %v", raw, err)
	}
	if len(plan.Rows) != 4 || plan.Rows[0].ID != "b1" {
		t.Fatalf("plan rows = %+v", plan.Rows)
	}
	held := 0
	for _, s := range skips {
		if s == SkipHeld {
			held++
		}
	}
	if held != 1 {
		t.Fatalf("held = %d, want 1 (b2)", held)
	}

	// Between plan and apply b1 is unchanged, b4 is deleted, and a person
	// approves b1, b3 and b4. b2 changed so that its fingerprint differs.
	delete(titles, "b4")
	titles["b2"] = "l Now"
	approved := []Row{plan.Rows[0], plan.Rows[1], plan.Rows[2], plan.Rows[3]}
	params, _ := json.Marshal(FixerApply{PlanRef: "plan-op", Rows: approved})
	w := &titleWriter{titles: titles}
	live := NewRC(RunEnv{Context: context.Background(), OpID: "apply", Mode: Live, Writer: w})
	job, err = Compile(def).OpenBatch(live, params)
	if err != nil {
		t.Fatal(err)
	}
	if !job.Partitioned || job.Items.Partition(0) != job.Items.Key(0) {
		t.Fatal("a Fixer apply partitions by Row.ID by default")
	}
	skips, errs, marks := runSetMarks(t, live, job.Items)
	for _, e := range errs {
		if e != nil {
			t.Fatalf("apply error: %v", e)
		}
	}
	got := map[string]string{}
	for i, s := range skips {
		got[job.Items.Key(i)] = s
	}
	want := map[string]string{"b1": "", "b2": SkipChangedSincePlan, "b3": "owner_manual", "b4": SkipGone}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("apply outcomes = %v, want %v", got, want)
	}
	if titles["b1"] != "1 Volume" || titles["b3"] != "l Book" {
		t.Fatalf("titles after apply = %v", titles)
	}
	raw, err = job.Finish(live, Summary{Marks: marks})
	var res FixerResult
	if err != nil || json.Unmarshal(raw, &res) != nil || !reflect.DeepEqual(res.Applied, []string{"b1"}) {
		t.Fatalf("apply result %s %v", raw, err)
	}
}

func TestPipelineRunsTheDAG(t *testing.T) {
	scan, enrich, report, cleanup := okTask("p.scan"), okTask("p.enrich"), okTask("p.report"), okTask("p.cleanup")
	type res struct{ N int }
	def := Pipeline("p.all", PipelineSpec[struct{ Limit int }]{
		Common: Common{Title: "all", Permission: auth.PermSettingsManage},
		Stages: []Stage[struct{ Limit int }]{
			{Name: "scan", Def: scan},
			{Name: "enrich", Def: enrich, After: []string{"scan"}, Params: func(p struct{ Limit int }, r Results) (any, error) {
				var got res
				if err := r.Decode("scan", &got); err != nil {
					return nil, err
				}
				return map[string]int{"limit": p.Limit, "found": got.N}, nil
			}},
			{Name: "report", Def: report, After: []string{"enrich"}},
			{Name: "cleanup", Def: cleanup, OnFail: ContinueOthers},
		}})
	h := &fakeHost{outcomes: map[string]Outcome{
		"p.scan":    {State: "completed", Result: json.RawMessage(`{"N":3}`)},
		"p.enrich":  {State: "failed", Err: "boom"},
		"p.cleanup": {State: "completed"},
	}}
	raw, err := Compile(def).RunTask(newTestRC(h, Live), json.RawMessage(`{"Limit":9}`))
	if err == nil || !strings.Contains(err.Error(), "enrich") {
		t.Fatalf("err = %v, want the enrich failure", err)
	}
	var out PipelineResult
	if json.Unmarshal(raw, &out) != nil {
		t.Fatalf("result %s", raw)
	}
	if out.Stages["report"].State != "skipped" || out.Stages["cleanup"].State != "completed" {
		t.Fatalf("stages = %+v", out.Stages)
	}
	if string(h.params["p.enrich"]) != `{"found":3,"limit":9}` {
		t.Fatalf("enrich params = %s", h.params["p.enrich"])
	}
	for _, id := range h.enqueued {
		if id == "p.report" {
			t.Fatal("report ran although the stage it waits on failed")
		}
	}
}
