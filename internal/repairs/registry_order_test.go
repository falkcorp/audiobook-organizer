// file: internal/repairs/registry_order_test.go
// version: 1.0.1
// guid: 4b7d2e91-3c58-4a06-9f1e-8d5a0c6b7e23
// last-edited: 2026-11-05

package repairs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// metaFixer is a fixer that only reports an id and a Meta.
type metaFixer struct {
	id   string
	meta Meta
}

func (f metaFixer) ID() string          { return f.id }
func (f metaFixer) Title() string       { return "Title of " + f.id }
func (f metaFixer) Description() string { return "Description of " + f.id }
func (f metaFixer) Meta() Meta          { return f.meta }
func (f metaFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	return nil, nil
}
func (f metaFixer) Replan(_ context.Context, _ json.RawMessage, r Row, _ registry.Reporter) (Row, error) {
	return r, nil
}
func (f metaFixer) Apply(context.Context, *Writer, Row) error { return nil }

func listedIDs(t *testing.T, fixers ...Fixer) []string {
	t.Helper()
	reg := NewRegistry()
	for _, f := range fixers {
		require.NoError(t, reg.Register(f))
	}
	var ids []string
	for _, f := range reg.List() {
		ids = append(ids, f.ID())
	}
	return ids
}

// List orders by group (the Groups order), then rank, then id: not by id and
// not by registration order.
func TestRegistryList_OrdersByGroupThenRankThenID(t *testing.T) {
	got := listedIDs(t,
		metaFixer{"bruce", Meta{Group: GroupImports, Rank: 1}},
		metaFixer{"alfred", Meta{Group: GroupSeries, Rank: 2}},
		metaFixer{"selina", Meta{Group: GroupDuplicates, Rank: 9}},
		metaFixer{"harvey", Meta{Group: GroupSeries, Rank: 1}},
		metaFixer{"barbara", Meta{Group: GroupDuplicates, Rank: 2}},
		// Same group and rank as "barbara": the id decides, so the order is
		// the same on every call.
		metaFixer{"alicia", Meta{Group: GroupDuplicates, Rank: 2}},
	)
	require.Equal(t, []string{"alicia", "barbara", "selina", "harvey", "alfred", "bruce"}, got)
}

// A fixer whose group is empty or not one of Groups() is listed after every
// real group (alphabetically among its kind), never dropped.
func TestRegistryList_UnknownGroupSortsLast(t *testing.T) {
	got := listedIDs(t,
		metaFixer{"talia", Meta{}},
		metaFixer{"edward", Meta{Group: "no-such-group", Rank: 1}},
		metaFixer{"oswald", Meta{Group: GroupImports, Rank: 3}},
		metaFixer{"jim", Meta{Group: GroupTitles, Rank: 1}},
	)
	require.Equal(t, []string{"jim", "oswald", "talia", "edward"}, got,
		"ungrouped fixers share the last slot and fall back to rank then id (talia rank 0, edward rank 1)")
}

// Different mistyped groups never interleave: each unknown group is one
// contiguous run, the runs ordered by the group string, whatever the ranks.
func TestRegistryList_UnknownGroupsStayContiguousByGroupString(t *testing.T) {
	got := listedIDs(t,
		metaFixer{"alfred", Meta{Group: "zz-typo", Rank: 1}},
		metaFixer{"barbara", Meta{Group: "aa-typo", Rank: 2}},
		metaFixer{"selina", Meta{Group: "zz-typo", Rank: 3}},
		metaFixer{"harvey", Meta{Group: "aa-typo", Rank: 4}},
		metaFixer{"jim", Meta{Group: GroupTitles, Rank: 1}},
	)
	require.Equal(t, []string{"jim", "barbara", "harvey", "alfred", "selina"}, got)
}

// The groups are in the owner's order (2026-11-05), the imports group last, and
// every group has a distinct id and a title.
func TestGroups_Order(t *testing.T) {
	var ids []GroupID
	seen := map[GroupID]bool{}
	for _, g := range Groups() {
		ids = append(ids, g.ID)
		require.NotEmpty(t, g.Title, g.ID)
		require.False(t, seen[g.ID], "group %q listed twice", g.ID)
		seen[g.ID] = true
		require.Equal(t, g.Title, GroupTitle(g.ID))
	}
	require.Equal(t, []GroupID{GroupDuplicates, GroupTitles, GroupAuthors, GroupSeries,
		GroupMetadata, GroupFiles, GroupVersions, GroupImports}, ids)
	require.Equal(t, "", GroupTitle("no-such-group"))

	// Groups hands out a copy: reordering it must not reorder the lane.
	gs := Groups()
	gs[0], gs[1] = gs[1], gs[0]
	require.Equal(t, GroupDuplicates, Groups()[0].ID)
}
