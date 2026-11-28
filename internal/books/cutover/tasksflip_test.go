// file: internal/books/cutover/tasksflip_test.go
// version: 0.1.0
// guid: 7c1d4e92-a5b3-4068-9e27-d3f8b0a61c45
// last-edited: 2026-11-22

package cutover

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m5"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// nopTaskImporter and nopLogImporter satisfy the two configs' non-nil checks;
// no test here runs a step, so no method is ever called.
type nopTaskImporter struct{ tasks.Importer }

func (nopTaskImporter) StampView(context.Context, *storage.Batch, tasks.Kind, tasks.KindView) error {
	return nil
}

type nopLogImporter struct{ log.Importer }

type nopRuns struct{}

func (nopRuns) Get(context.Context, tasks.RunID) (tasks.RunRecord, error) {
	return tasks.RunRecord{}, errors.New("not called")
}

// tasksOpts are the tasks flip's inputs over stubs.
func tasksOpts() *TasksFlipOptions {
	return &TasksFlipOptions{
		Runs: m4.Config{
			Importer:         nopTaskImporter{},
			Legacy:           func() storage.Reader { return nil },
			Reconcile:        func(context.Context) error { return nil },
			ViewOf:           func(tasks.Kind) (tasks.KindView, bool, error) { return tasks.KindView{}, false, nil },
			ScanStandDownKey: "books.library.scan",
			Location:         time.UTC,
			StampAt:          time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC),
		},
		Activity: m4.ActivityConfig{Runs: func() m4.RunReader { return nopRuns{} }},
		Log:      m5.Config{Importer: nopLogImporter{}, Logger: slog.New(slog.DiscardHandler)},
	}
}

func tasksFlip(t testing.TB) Flip {
	t.Helper()
	opts := flipOpts(t, true)
	opts.Tasks = tasksOpts()
	f, err := NewFlip(nil, nil, opts)
	if err != nil {
		t.Fatalf("NewFlip(tasks): %v", err)
	}
	return f
}

func ref(id string) storage.StepRef { return storage.StepRef{Owner: BooksOwner, DataMigration: id} }

// TestFlip_TasksOnListIsValid: with the tasks flip on, NewFlip registers
// books.level-1, M2, books.level-2, M3, books.level-3, M4 and M5 in that order
// with M8 last, and ValidateCutoverList accepts it against books level 3 and
// refuses it against level 2 (the last level step raises to 3).
func TestFlip_TasksOnListIsValid(t *testing.T) {
	f := tasksFlip(t)
	l := flipList(f, BooksTasksLevel)
	if err := ValidateCutoverList(l); err != nil {
		t.Fatalf("the tasks-on list is invalid: %v", err)
	}
	want := []string{"core.level-1", BooksCutoverLevelStepID, ConfigCutoverID, BooksIdentityLevelStepID, IdentityCutoverID,
		BooksTasksLevelStepID, TasksCutoverID, LogCutoverID, RewriteID}
	if got := entryNames(l.Entries); !slices.Equal(got, want) {
		t.Errorf("tasks-on entries = %q, want %q", got, want)
	}
	if err := ValidateCutoverList(flipList(f, BooksIdentityLevel)); err == nil {
		t.Error("the tasks-on list passed against books level 2, but its last level step raises to 3")
	}
}

// TestFlip_TasksStepsLevelAndAfter: M4 and M5 raise books to level 3 behind
// books.level-3 (the version after books.level-2's); M4 also runs after M3, the
// activity rewrite after M4, M5 after M4; the activity rewrite is registered
// beside the list's data steps, not in it; M8 runs after all of them.
func TestFlip_TasksStepsLevelAndAfter(t *testing.T) {
	f := tasksFlip(t)
	if got, want := dataIDs(f.Data), []string{ConfigCutoverID, IdentityCutoverID, TasksCutoverID, LogCutoverID}; !slices.Equal(got, want) {
		t.Fatalf("data steps %v, want %v", got, want)
	}
	if got := dataIDs(f.InPlace); !slices.Equal(got, []string{TasksActivityRewriteID}) {
		t.Fatalf("in-place steps %v, want only the activity rewrite", got)
	}
	level3 := BooksLevelStepRef(booksLevelVersion + 2)
	m4step, m5step, act := f.Data[2], f.Data[3], f.InPlace[0]
	if !slices.Equal(stepRefs(m4step.After...), stepRefs(level3, ref(IdentityCutoverID))) {
		t.Errorf("M4 After = %v, want books.level-3 and M3", stepRefs(m4step.After...))
	}
	if !slices.Equal(stepRefs(m5step.After...), stepRefs(level3, ref(TasksCutoverID))) {
		t.Errorf("M5 After = %v, want books.level-3 and M4", stepRefs(m5step.After...))
	}
	if !slices.Equal(stepRefs(act.After...), stepRefs(level3, ref(IdentityCutoverID), ref(TasksCutoverID))) {
		t.Errorf("activity After = %v, want books.level-3, M3 and M4", stepRefs(act.After...))
	}
	for _, d := range []struct {
		id    string
		level int
	}{{m4step.ID, m4step.RaisesLevel}, {m5step.ID, m5step.RaisesLevel}, {act.ID, act.RaisesLevel}} {
		if d.level != BooksTasksLevel {
			t.Errorf("%s raises %d, want %d", d.id, d.level, BooksTasksLevel)
		}
	}
	if act.Cutover {
		t.Error("the activity rewrite is a Cutover step; its rows stay a read-write claim, so ValidateCutoverList would refuse it among the data entries")
	}
	wantM8 := stepRefs(ref(ConfigCutoverID), ref(IdentityCutoverID), ref(TasksCutoverID), ref(TasksActivityRewriteID), ref(LogCutoverID))
	if !slices.Equal(stepRefs(f.Rewrite.After...), wantM8) {
		t.Errorf("M8 After = %v, want %v", stepRefs(f.Rewrite.After...), wantM8)
	}
	// The pure shape the manifest builds with no importers is the same M8.
	opts := flipOpts(t, true)
	opts.Tasks = &TasksFlipOptions{}
	shape := NewFlipShape(opts)
	if !slices.Equal(stepRefs(shape.Rewrite.After...), wantM8) || len(shape.Levels) != 3 || len(shape.Data) != 0 {
		t.Errorf("shape: %d levels, %d data, M8 After %v", len(shape.Levels), len(shape.Data), stepRefs(shape.Rewrite.After...))
	}
}

// TestFlip_TasksNeedsIdentity: level 3 is built over level 2 and M4 runs after
// M3, so the tasks flip without the identity flip is refused, not built.
func TestFlip_TasksNeedsIdentity(t *testing.T) {
	opts := flipOpts(t, false)
	opts.Tasks = tasksOpts()
	_, err := NewFlip(nil, nil, opts)
	if err == nil || !strings.Contains(err.Error(), "needs FlipOptions.Identity") {
		t.Fatalf("NewFlip(tasks without identity) = %v, want the refusal", err)
	}
}

// TestFlip_TasksSpec: Spec declares level 3 with books.level-3 at the version
// after books.level-2's, retires every ops-family claim under M4's source
// prefixes and the syslog: claim, and adds retired sub-claims of the shared
// setting: family for the rows M4 moves; every M4 and M5 source range then lies
// inside a retired claim.
func TestFlip_TasksSpec(t *testing.T) {
	f := tasksFlip(t)
	base := store.BooksSpec(nil)
	spec := f.Spec(base)
	if spec.Level != BooksTasksLevel {
		t.Errorf("Spec.Level = %d, want %d", spec.Level, BooksTasksLevel)
	}
	tail := spec.Migrations[len(base.Migrations):]
	if len(tail) != 3 {
		t.Fatalf("Spec appended %d steps, want 3", len(tail))
	}
	if m := tail[2]; m.Name != BooksTasksLevelStepID || m.Version != booksLevelVersion+2 || m.RaisesLevel != BooksTasksLevel ||
		!slices.Equal(stepRefs(m.After...), stepRefs(BooksLevelStepRef(booksLevelVersion+1))) {
		t.Errorf("third level step = %s v%d raising %d after %v", m.Name, m.Version, m.RaisesLevel, stepRefs(m.After...))
	}
	var retired []storage.Claim
	for _, c := range spec.Claims {
		if c.Mode == storage.ClaimRetired {
			retired = append(retired, c)
		}
	}
	inRetired := func(r storage.Range) bool {
		return slices.ContainsFunc(retired, func(c storage.Claim) bool {
			lo, hi := c.Prefix, ""
			if c.Key != "" {
				lo, hi = c.Key, c.Key+"\x00"
			} else {
				hi = string(prefixEndOf(c.Prefix))
			}
			return string(r.Start) >= lo && string(r.End) <= hi
		})
	}
	for _, r := range slices.Concat(m4.SourceRanges(), m5.SourceRanges()) {
		if !inRetired(r) {
			t.Errorf("source range [%q, %q) lies in no retired claim", r.Start, r.End)
		}
	}
	// The shared setting: family stays read-write: only the moved rows retire.
	i := slices.IndexFunc(spec.Claims, func(c storage.Claim) bool { return c.Prefix == "setting:" })
	if i < 0 || spec.Claims[i].Mode != storage.ClaimReadWrite {
		t.Errorf("the setting: family claim is not read-write (index %d)", i)
	}
}

func prefixEndOf(p string) []byte {
	end := []byte(p)
	end[len(end)-1]++
	return end
}
