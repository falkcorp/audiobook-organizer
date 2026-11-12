// file: core/backup/journal/hold_test.go
// version: 1.0.0
// guid: b696000e-8360-40f8-ad9d-62dfe60176af
// last-edited: 2026-11-12

package journal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// boundComponent is a component over fx's store bound to its journal file.
func (fx *fixture) boundComponent(t *testing.T) *Component {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	c := fx.component(t)
	must(t, c.Bind(context.Background(), fx.path))
	return c
}

// everyWriter tries each way a line reaches the journal and returns what each
// said, in the order Append, AppendLine, WriteLine.
func everyWriter(t *testing.T, fx *fixture, c *Component) [3]error {
	t.Helper()
	ctx := context.Background()
	var out [3]error
	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }()
	out[0] = c.Append(ctx, b, Revocation{Kind: UserDisabled})
	_, out[1] = c.AppendLine(ctx, GapLine(time.Now(), GapAppendFailed))
	_, out[2] = c.WriteLine(ctx, GapLine(time.Now(), GapAppendFailed))
	return out
}

// TestComponentHold_ResumeLiftsItAndABreakOutlastsIt: a Hold is the refusal that
// ends (a replaced store's head waits for its hooks). Every writer of the
// component refuses with the first hold's error and health says unwritable until
// Resume; Hold(nil) holds nothing; a journal that is broken says so, never the
// hold, and Resume does not mend it.
func TestComponentHold_ResumeLiftsItAndABreakOutlastsIt(t *testing.T) {
	fx := newFixture(t)
	c := fx.boundComponent(t)
	held := errors.New("the hooks have not run")

	c.Hold(nil)
	for i, err := range everyWriter(t, fx, c) {
		if err != nil {
			t.Fatalf("writer %d after Hold(nil): %v", i, err)
		}
	}
	c.Hold(held)
	c.Hold(errors.New("a second hold"))
	for i, err := range everyWriter(t, fx, c) {
		if !errors.Is(err, held) {
			t.Errorf("writer %d while held = %v, want the first hold's error", i, err)
		}
	}
	got := c.Health()
	if len(got) == 0 || got[0].Token != HealthUnwritable || got[0].Detail != held.Error() {
		t.Errorf("health while held = %+v, want unwritable naming the hold", got)
	}
	c.Resume()
	for i, err := range everyWriter(t, fx, c) {
		if err != nil {
			t.Errorf("writer %d after Resume: %v", i, err)
		}
	}
	for _, h := range c.Health() {
		if h.Token == HealthUnwritable {
			t.Errorf("health after Resume still reports %+v", h)
		}
	}

	// A broken file refuses with its own error, held or not.
	fx2 := newFixture(t)
	fx2.write(t, "this is not a journal line")
	bc := fx2.component(t)
	if err := bc.Bind(context.Background(), fx2.path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Bind = %v, want ErrCorrupt", err)
	}
	bc.Hold(held)
	for i, err := range everyWriter(t, fx2, bc) {
		if !errors.Is(err, ErrCorrupt) || errors.Is(err, held) {
			t.Errorf("broken and held, writer %d = %v, want ErrCorrupt and not the hold", i, err)
		}
	}
	bc.Resume()
	for i, err := range everyWriter(t, fx2, bc) {
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("broken, after Resume, writer %d = %v, want ErrCorrupt", i, err)
		}
	}
}

// TestComponentHold_SurvivesBind: the hold is the component's, not the file's,
// so a Bind (which replaces the file) neither drops it nor needs the file to
// exist when it is set. A boot that holds the journal and then re-binds it (the
// move of the journal to a new directory) is still held.
func TestComponentHold_SurvivesBind(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	c := fx.component(t)
	held := errors.New("the hooks have not run")

	c.Hold(held) // before Bind: it is not lost for want of a file
	if _, err := c.WriteLine(ctx, GapLine(time.Now(), GapAppendFailed)); !errors.Is(err, ErrNotBound) {
		t.Errorf("an unbound component's WriteLine = %v, want ErrNotBound", err)
	}
	must(t, c.Bind(ctx, fx.path))
	for i, err := range everyWriter(t, fx, c) {
		if !errors.Is(err, held) {
			t.Errorf("writer %d after Bind = %v, want the hold's error", i, err)
		}
	}
	other := filepath.Join(fx.dir, "elsewhere", "gotham_revocations.journal")
	must(t, os.MkdirAll(filepath.Dir(other), 0o700))
	must(t, c.Bind(ctx, other)) // replaces the bound file
	for i, err := range everyWriter(t, fx, c) {
		if !errors.Is(err, held) {
			t.Errorf("writer %d after a second Bind = %v, want the hold's error", i, err)
		}
	}
	c.Resume()
	for i, err := range everyWriter(t, fx, c) {
		if err != nil {
			t.Errorf("writer %d after Resume = %v", i, err)
		}
	}
}

// TestComponentHold_ARefusedAppendStagesNothing: a revocation refused by a hold
// stages no head into the caller's batch, and the caller's Gap still works, so the
// revocation commits with its gap record as for any append failure.
func TestComponentHold_ARefusedAppendStagesNothing(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	c := fx.boundComponent(t)
	held := errors.New("the hooks have not run")
	c.Hold(held)
	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }()
	must(t, b.Set([]byte("user/bane"), []byte("disabled")))
	if err := c.Append(ctx, b, Revocation{Kind: UserDisabled}); !errors.Is(err, held) {
		t.Fatalf("Append = %v, want the hold's error", err)
	}
	if b.Count() != 1 {
		t.Errorf("the refused Append staged into the batch: count %d, want 1 (the caller's own write)", b.Count())
	}
	must(t, c.Gap(b, time.Now()))
	must(t, b.Commit(ctx, storage.Sync))
	if h := fx.head(t); h.Seq != 0 {
		t.Errorf("head = %d after a refused Append, want 0", h.Seq)
	}
	if open, err := OpenGaps(ctx, fx.ks); err != nil || !open {
		t.Errorf("OpenGaps = %v, %v, want the gap recorded", open, err)
	}
}
