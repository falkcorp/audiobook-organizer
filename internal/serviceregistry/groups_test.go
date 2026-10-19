// file: internal/serviceregistry/groups_test.go
// version: 1.0.1
// last-edited: 2026-10-19

package serviceregistry

import (
	"reflect"
	"testing"
)

func TestIncludeGroup_PullsMembersOnly(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{
		Name:   "a",
		Groups: []string{"alpha"},
		Build:  func(c *Container) (any, error) { return "a", nil },
	})
	Register(ServiceDef{
		Name:   "b",
		Groups: []string{"alpha", "beta"},
		Build:  func(c *Container) (any, error) { return "b", nil },
	})
	Register(ServiceDef{
		Name:   "c",
		Groups: []string{"beta"},
		Build:  func(c *Container) (any, error) { return "c", nil },
	})
	Register(ServiceDef{
		Name:  "d", // no groups — shouldn't be pulled by any IncludeGroup call
		Build: func(c *Container) (any, error) { return "d", nil },
	})

	c := NewContainer().IncludeGroup("alpha")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	want := []string{"a", "b"} // both in "alpha"; c is "beta"-only; d ungrouped
	if !reflect.DeepEqual(got, want) {
		t.Errorf("IncludeGroup(\"alpha\") order = %v, want %v", got, want)
	}
}

func TestIncludeGroup_MultipleGroupsUnion(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "a", Groups: []string{"alpha"}, Build: func(c *Container) (any, error) { return nil, nil }})
	Register(ServiceDef{Name: "b", Groups: []string{"beta"}, Build: func(c *Container) (any, error) { return nil, nil }})
	Register(ServiceDef{Name: "c", Groups: []string{"gamma"}, Build: func(c *Container) (any, error) { return nil, nil }})

	c := NewContainer().IncludeGroup("alpha", "beta")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("IncludeGroup(union) = %v, want %v", got, want)
	}
}

func TestIncludeGroup_UnknownGroupIsNoop(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "a", Groups: []string{"real"}, Build: func(c *Container) (any, error) { return nil, nil }})

	c := NewContainer().IncludeGroup("nonexistent")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	if len(got) != 0 {
		t.Errorf("IncludeGroup(unknown) included %v, want []", got)
	}
}

func TestIncludeGroup_TransitiveDepsPulledIn(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	// "leaf" is NOT in any group; "top" is in "alpha" and Needs "leaf".
	// IncludeGroup("alpha") should pull "top" AND its transitive dep "leaf",
	// even though "leaf" isn't in the group.
	Register(ServiceDef{Name: "leaf", Build: func(c *Container) (any, error) { return "leaf", nil }})
	Register(ServiceDef{
		Name:   "top",
		Needs:  []string{"leaf"},
		Groups: []string{"alpha"},
		Build:  func(c *Container) (any, error) { return "top", nil },
	})

	c := NewContainer().IncludeGroup("alpha")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	want := []string{"leaf", "top"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("transitive: got %v, want %v", got, want)
	}
}

func TestIncludeGroup_Composable(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(ServiceDef{Name: "g", Groups: []string{"core"}, Build: func(c *Container) (any, error) { return nil, nil }})
	Register(ServiceDef{Name: "h", Build: func(c *Container) (any, error) { return nil, nil }})

	// Mix IncludeGroup with explicit Include
	c := NewContainer().IncludeGroup("core").Include("h")
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := c.Names()
	want := []string{"g", "h"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("composable: got %v, want %v", got, want)
	}
}
