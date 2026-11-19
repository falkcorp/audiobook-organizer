// file: core/internal/executor/writer_stamps_test.go
// version: 1.0.0
// guid: 4c0e7f2a-6d13-4b9e-8a55-1f7b3c9d2e60
// last-edited: 2026-11-19

package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// stampOutcome is what one Do of a stamp-set test saw.
type stampOutcome struct {
	err    error
	called bool
}

// TestWriter_FenceChecksEveryStamp (F5-18b, O17/O27): a gated write may name
// several resources (tasks.Write.Resources, one Do for a store call whose row
// is several resources), and the fence admits it only when the definition
// declares EVERY one of them. The undeclared stamp is refused whether it is
// first, in the middle or last, whether the write is a Preview or a Live one,
// and whether the legacy single Resource names it or the stamp set does. A
// refused write never calls fn and never records an intent: the fence is
// checked before both, not inferred from what fn changed.
func TestWriter_FenceChecksEveryStamp(t *testing.T) {
	cases := tasks.Res("gotham", "cases")
	clues := tasks.Res("gotham", "clues")
	vault := tasks.Res("gotham", "vault")
	rogue := tasks.Res("gotham", "rogues_gallery")
	declaredEffects := func(c *tasks.Common) { c.Effects = tasks.Writes(cases, clues, vault) }

	type cell struct {
		name     string
		write    tasks.Write
		wantErr  bool
		mention  string // the refused resource the error must name
		intents  int
		fnCalled bool
	}
	subject := tasks.ResID("gotham", "cases", "ace")
	w := func(res tasks.Resource, more ...tasks.Resource) tasks.Write {
		return tasks.Write{Name: "gotham.close_case", Resource: res, Resources: more, Subject: subject}
	}
	cells := []cell{
		{name: "all declared (legacy field plus set)", write: w(cases, clues, vault), intents: 1, fnCalled: true},
		{name: "all declared (set only)", write: tasks.Write{Name: "gotham.close_case", Resources: []entity.Ref{cases, clues, vault}, Subject: subject}, intents: 1, fnCalled: true},
		{name: "all declared (legacy field only)", write: w(cases), intents: 1, fnCalled: true},
		{name: "duplicate stamps are one stamp", write: w(cases, cases, cases), intents: 1, fnCalled: true},
		{name: "undeclared in the legacy field", write: w(rogue, clues, vault), wantErr: true, mention: "rogues_gallery"},
		{name: "undeclared first in the set", write: w(cases, rogue, clues, vault), wantErr: true, mention: "rogues_gallery"},
		{name: "undeclared in the middle of the set", write: w(cases, clues, rogue, vault), wantErr: true, mention: "rogues_gallery"},
		{name: "undeclared last in the set", write: w(cases, clues, vault, rogue), wantErr: true, mention: "rogues_gallery"},
		{name: "undeclared alone in the set", write: tasks.Write{Name: "gotham.close_case", Resources: []entity.Ref{rogue}, Subject: subject}, wantErr: true, mention: "rogues_gallery"},
		{name: "undeclared module", write: w(cases, tasks.Res("arkham", "clues")), wantErr: true, mention: "arkham/clues"},
		{name: "no stamp at all", write: tasks.Write{Name: "gotham.close_case", Subject: subject}, wantErr: true, mention: "no resource"},
		{name: "a zero ref inside the set", write: w(cases, entity.Ref{}), wantErr: true, mention: "/"},
	}
	for _, mode := range []tasks.ModeRequest{tasks.ModeRequestPreview, tasks.ModeRequestLive} {
		for _, c := range cells {
			t.Run(string(rune('0'+int(mode)))+"/"+c.name, func(t *testing.T) {
				got := make(chan stampOutcome, 1)
				def := taskDef("gotham.closecase", func(rc *tasks.RC, _ noParams) (string, error) {
					var o stampOutcome
					o.err = rc.Writer().Do(rc.Context(), c.write, func(context.Context) ([]tasks.Change, error) {
						o.called = true
						return nil, nil
					})
					got <- o
					return "filed", nil
				}, declaredEffects)
				h := newHarness(t, []tasks.Definition{def}, nil)
				id := h.start("gotham.closecase", tasks.StartRequest{Mode: mode})
				h.waitState(id, tasks.StateSucceeded)
				o := <-got
				preview := mode == tasks.ModeRequestPreview
				if c.wantErr {
					if !errors.Is(o.err, tasks.ErrOutsideEffects) {
						t.Fatalf("Do = %v, want ErrOutsideEffects", o.err)
					}
					if !strings.Contains(o.err.Error(), c.mention) {
						t.Errorf("error %q does not name %q", o.err, c.mention)
					}
					if o.called {
						t.Error("a refused write called fn")
					}
					if n := h.intentsOf(id); n != 0 {
						t.Errorf("a refused write recorded %d intents, want none", n)
					}
					return
				}
				if preview {
					if !errors.Is(o.err, tasks.ErrPreview) || o.called {
						t.Errorf("Preview: Do = %v, called %v; want ErrPreview, not called", o.err, o.called)
					}
				} else if o.err != nil || o.called != c.fnCalled {
					t.Errorf("Live: Do = %v, called %v; want nil, called", o.err, o.called)
				}
				if n := h.intentsOf(id); n != c.intents {
					t.Errorf("intents = %d, want %d", n, c.intents)
				}
			})
		}
	}
}

// TestWriter_IntentRecordsEveryStamp: the write-ahead intent keeps the whole
// stamp set, so a reader of the run's evidence sees every resource the write
// was fenced against, not only the first.
func TestWriter_IntentRecordsEveryStamp(t *testing.T) {
	cases := tasks.Res("gotham", "cases")
	clues := tasks.Res("gotham", "clues")
	def := taskDef("gotham.closecase", func(rc *tasks.RC, _ noParams) (string, error) {
		_ = rc.Writer().Do(rc.Context(), tasks.Write{Name: "gotham.close_case", Resource: cases, Resources: []entity.Ref{clues}, Subject: tasks.ResID("gotham", "cases", "ace")},
			func(context.Context) ([]tasks.Change, error) { return nil, nil })
		return "filed", nil
	}, func(c *tasks.Common) { c.Effects = tasks.Writes(cases, clues) })
	h := newHarness(t, []tasks.Definition{def}, nil)
	id := h.start("gotham.closecase", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	h.waitState(id, tasks.StateSucceeded)
	var found []tasks.Write
	for p, err := range h.stores.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageIntents}) {
		if err != nil {
			t.Fatal(err)
		}
		found = append(found, p.Write)
	}
	if len(found) != 1 || len(found[0].Resources) != 1 || found[0].Resources[0] != clues || found[0].Resource != cases {
		t.Fatalf("intents %+v, want the one write with both stamps", found)
	}
}

// TestWriter_FenceHonoursIDRefs: a declared ref with an ID covers only that ID,
// a declared kind-wide ref covers every ID of its kind, and a stamp that names
// the whole kind is not covered by a declaration of one ID of it.
func TestWriter_FenceHonoursIDRefs(t *testing.T) {
	ace := tasks.ResID("gotham", "cases", "ace")
	joker := tasks.ResID("gotham", "cases", "joker")
	kindWide := tasks.Res("gotham", "cases")
	cells := []struct {
		name     string
		declared tasks.Resource
		stamp    tasks.Resource
		admitted bool
	}{
		{"an ID covers itself", ace, ace, true},
		{"an ID does not cover another ID", ace, joker, false},
		{"an ID does not cover its whole kind", ace, kindWide, false},
		{"a kind covers any of its IDs", kindWide, joker, true},
		{"a kind covers itself", kindWide, kindWide, true},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			got := make(chan stampOutcome, 1)
			def := taskDef("gotham.closecase", func(rc *tasks.RC, _ noParams) (string, error) {
				var o stampOutcome
				o.err = rc.Writer().Do(rc.Context(), tasks.Write{Name: "gotham.close_case", Resources: []entity.Ref{c.stamp}, Subject: joker},
					func(context.Context) ([]tasks.Change, error) { o.called = true; return nil, nil })
				got <- o
				return "filed", nil
			}, func(cm *tasks.Common) { cm.Effects = tasks.Writes(c.declared) })
			h := newHarness(t, []tasks.Definition{def}, nil)
			id := h.start("gotham.closecase", tasks.StartRequest{Mode: tasks.ModeRequestLive})
			h.waitState(id, tasks.StateSucceeded)
			o := <-got
			if c.admitted && (o.err != nil || !o.called) {
				t.Errorf("Do = %v, called %v; want admitted", o.err, o.called)
			}
			if !c.admitted && (!errors.Is(o.err, tasks.ErrOutsideEffects) || o.called) {
				t.Errorf("Do = %v, called %v; want ErrOutsideEffects, not called", o.err, o.called)
			}
		})
	}
}
