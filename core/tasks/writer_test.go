// file: core/tasks/writer_test.go
// version: 1.0.0
// guid: 8e1b6d34-2f07-4a9c-b5d8-6c3a90e47f12
// last-edited: 2026-11-19

package tasks_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

func TestWrite_StampsIsResourceThenResourcesWithoutRepeats(t *testing.T) {
	a, b, c := tasks.Res("gotham", "a"), tasks.Res("gotham", "b"), tasks.Res("gotham", "c")
	for _, tc := range []struct {
		name string
		w    tasks.Write
		want []entity.Ref
	}{
		{"unset", tasks.Write{}, []entity.Ref{}},
		{"legacy field only", tasks.Write{Resource: a}, []entity.Ref{a}},
		{"set only", tasks.Write{Resources: []entity.Ref{a, b}}, []entity.Ref{a, b}},
		{"field then set, repeat dropped", tasks.Write{Resource: b, Resources: []entity.Ref{a, b, c, a}}, []entity.Ref{b, a, c}},
		{"a zero ref in the set is kept", tasks.Write{Resource: a, Resources: []entity.Ref{{}}}, []entity.Ref{a, {}}},
	} {
		if got := tc.w.Stamps(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Stamps = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckWrite_NamesEveryRefusedStamp(t *testing.T) {
	cases, clues := tasks.Res("gotham", "cases"), tasks.Res("gotham", "clues")
	declared := []entity.Ref{cases}
	if err := tasks.CheckWrite("gotham.k", declared, tasks.Write{Resource: cases}); err != nil {
		t.Fatalf("a declared stamp: %v", err)
	}
	err := tasks.CheckWrite("gotham.k", declared, tasks.Write{Resource: cases, Resources: []entity.Ref{clues, tasks.Res("arkham", "files")}})
	if !errors.Is(err, tasks.ErrOutsideEffects) {
		t.Fatalf("an undeclared stamp: %v", err)
	}
	for _, want := range []string{"gotham.k", "gotham/clues", "arkham/files"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "gotham/cases") {
		t.Errorf("error %q names a declared stamp as refused", err)
	}
	if err := tasks.CheckWrite("gotham.k", declared, tasks.Write{Name: "gotham.note"}); !errors.Is(err, tasks.ErrOutsideEffects) || !strings.Contains(err.Error(), "no resource") {
		t.Errorf("a write that names nothing: %v", err)
	}
}
