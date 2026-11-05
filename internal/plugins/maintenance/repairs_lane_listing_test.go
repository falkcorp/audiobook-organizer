// file: internal/plugins/maintenance/repairs_lane_listing_test.go
// version: 1.0.0
// guid: 7e1c9a54-2b60-4d83-a5f7-0c8d3b6e9142
// last-edited: 2026-11-05

package maintenance

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// laneOrder is the Repairs lane as the owner chose it on 2026-11-05: eight
// groups ordered by how often their fixers were applied, each group's fixers
// most used first, the swapped title/author fixer directly ahead of the
// junk-title fixer (it must run first), and the API-only tools last. The ids
// never change; the titles are display names only.
var laneOrder = []struct {
	id    string
	group repairs.GroupID
	title string
}{
	{fragFixerID, repairs.GroupDuplicates, "Merge chapter files into their book"},
	{"folder-books", repairs.GroupDuplicates, "Break up folder-sized books"},
	{"consolidation-leftovers", repairs.GroupDuplicates, "Merge leftover one-chapter books"},
	{"duplicate-copies", repairs.GroupDuplicates, "Merge duplicate copies of a book"},

	{"maintenance.repair-swapped-title-author", repairs.GroupTitles, "Fix swapped title and author"},
	{"maintenance.repair-junk-titles", repairs.GroupTitles, "Fix junk titles"},
	{"maintenance.reparse-folder-names", repairs.GroupTitles, "Fix titles parsed wrongly from folder names"},

	{"maintenance.repair-combined-author-credits", repairs.GroupAuthors, "Split combined author credits"},
	{"maintenance.repair-junk-authors", repairs.GroupAuthors, "Fix junk authors"},

	{"maintenance.author-named-series", repairs.GroupSeries, "Remove series named after the author"},
	{"dedup.series-prune", repairs.GroupSeries, "Merge duplicate series, remove empty ones"},

	{"maintenance.refetch-lost-candidates", repairs.GroupMetadata, "Refetch lost metadata matches"},
	{"maintenance.tag-franchise", repairs.GroupMetadata, "Tag Doctor Who, Big Finish and Torchwood"},
	{"maintenance.version-twin-metadata", repairs.GroupMetadata, "Copy metadata from another version"},

	{"stale-itunes-path", repairs.GroupFiles, "Clear stale iTunes paths"},
	{"reconcile.missing-files", repairs.GroupFiles, "Relink missing files to files on disk"},

	{"version-group-primary-repair", repairs.GroupVersions, "Pick the main version of each book"},
	{"retired-versions", repairs.GroupVersions, "Retire trashed books into their survivor"},
	{"maintenance.relink-pre-fix-imports", repairs.GroupVersions, "Relink old imports to their versions"},

	{"maintenance.scan-title-revert", repairs.GroupImports, "Revert titles a scan rewrote"},
	{"maintenance.audible-read-status", repairs.GroupImports, "Import read status from Audible"},
	{"master-list-import", repairs.GroupImports, "Import read status from a master list"},
}

// The 22 fixers the plugin registers are listed in exactly the owner's order,
// each under its group, and the list is the same on every call.
func TestRepairsLane_ListingOrderAndGroups(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	list := p.Repairs().List()
	require.Len(t, list, len(laneOrder), "every registered fixer is placed, none is missing from laneOrder")

	for i, want := range laneOrder {
		got := list[i]
		require.Equal(t, want.id, got.ID(), "position %d", i+1)
		require.Equal(t, want.group, got.Meta().Group, "%s group", want.id)
		require.Equal(t, want.title, got.Title(), "%s title", want.id)
	}

	again := p.Repairs().List()
	for i := range list {
		require.Equal(t, list[i].ID(), again[i].ID())
	}
}

// Group headers come out in the Groups order and each group is one run: a
// group never reappears after another one.
func TestRepairsLane_GroupsAreContiguousAndInOrder(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	var runs []repairs.GroupID
	for _, f := range p.Repairs().List() {
		g := f.Meta().Group
		if len(runs) == 0 || runs[len(runs)-1] != g {
			runs = append(runs, g)
		}
	}
	var want []repairs.GroupID
	for _, g := range repairs.Groups() {
		want = append(want, g.ID)
	}
	require.Equal(t, want, runs)
	require.Equal(t, repairs.GroupImports, runs[len(runs)-1], "imports and one-shot tools are always last")
}

var (
	// verbs are the fixed verb list of the naming rule; each has one meaning
	// across the lane.
	laneVerbs = []string{"Merge", "Break up", "Fix", "Split", "Remove", "Relink", "Clear", "Copy",
		"Pick", "Retire", "Tag", "Refetch", "Import", "Revert"}
	// laneJargon is what a title must not say: say what the owner sees instead.
	laneJargon = []string{"fragment", "primary", "object", "ordinal", "pre-fix", "version-group", "candidates", "consolidation"}
	// fixerIDShape matches an id such as "maintenance.repair-junk-titles".
	fixerIDShape = regexp.MustCompile(`\b(maintenance|dedup|reconcile)\.[a-z]+(-[a-z]+)*\b`)
)

// Every fixer has a group the lane knows, a rank unique inside it, a title that
// follows the naming rule and a one-line summary free of ids.
func TestRepairsLane_EveryFixerHasGroupRankTitleAndSummary(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	known := map[repairs.GroupID]bool{}
	for _, g := range repairs.Groups() {
		known[g.ID] = true
	}
	ranks := map[repairs.GroupID]map[int]string{}
	for _, f := range p.Repairs().List() {
		id, m, title := f.ID(), f.Meta(), f.Title()

		require.True(t, known[m.Group], "%s: group %q is not one of repairs.Groups()", id, m.Group)
		require.Positive(t, m.Rank, "%s: rank counts from 1", id)
		if ranks[m.Group] == nil {
			ranks[m.Group] = map[int]string{}
		}
		require.Empty(t, ranks[m.Group][m.Rank], "%s and %s share rank %d in %s", id, ranks[m.Group][m.Rank], m.Rank, m.Group)
		ranks[m.Group][m.Rank] = id

		require.NotEmpty(t, strings.TrimSpace(m.Summary), "%s: no summary", id)
		require.NotEmpty(t, strings.TrimSpace(f.Description()), "%s: no description", id)
		require.LessOrEqual(t, len([]rune(m.Summary)), 140, "%s: the summary is one line", id)
		require.False(t, fixerIDShape.MatchString(m.Summary), "%s: the summary names a fixer id", id)
		require.NotEqual(t, m.Summary, f.Description(), "%s: the summary is not the long description", id)

		require.LessOrEqual(t, len([]rune(title)), 45, "%s: title %q is too long", id, title)
		require.False(t, strings.ContainsAny(title, "()[]"), "%s: title %q has a bracket", id, title)
		require.False(t, fixerIDShape.MatchString(title), "%s: title %q names an id", id, title)
		for _, j := range laneJargon {
			require.NotContains(t, strings.ToLower(title), j, "%s: title %q uses jargon", id, title)
		}
		hasVerb := false
		for _, v := range laneVerbs {
			if strings.HasPrefix(title, v+" ") {
				hasVerb = true
			}
		}
		require.True(t, hasVerb, "%s: title %q does not start with a verb from the list", id, title)
		require.False(t, strings.Contains(strings.ToLower(title), "preview"), "%s: a state is a badge, not part of the title", id)
	}
}

// Nothing the lane shows names a fixer by its id: a description that points at
// another fixer does it by that fixer's title. (A description may still cite a
// maintenance job's id, which is not a fixer and has no title here.)
func TestRepairsLane_TextNamesFixersByTitleNotID(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	list := p.Repairs().List()
	for _, f := range list {
		text := map[string]string{"title": f.Title(), "description": f.Description(),
			"summary": f.Meta().Summary, "api only": f.Meta().APIOnly}
		for what, got := range text {
			for _, other := range list {
				require.NotContains(t, got, other.ID(), "%s %s names the fixer %q by id", f.ID(), what, other.ID())
			}
		}
	}
}

// The three fixers whose plan refuses to start without params cannot be started
// from the lane (it sends none), so each says it is API only and why. Every
// other fixer plans with no params and must not claim otherwise.
func TestRepairsLane_APIOnlyFixers(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	apiOnly := map[string]bool{
		scanTitleRevertFixerID:   true,
		audibleReadStatusFixerID: true,
		masterListImportFixerID:  true,
	}
	for _, f := range p.Repairs().List() {
		m := f.Meta()
		if apiOnly[f.ID()] {
			require.NotEmpty(t, strings.TrimSpace(m.APIOnly), "%s needs params the lane cannot send", f.ID())
			require.False(t, fixerIDShape.MatchString(m.APIOnly), "%s: the tooltip names an id", f.ID())
			require.Equal(t, repairs.GroupImports, m.Group, f.ID())
		} else {
			require.Empty(t, m.APIOnly, "%s plans with no params", f.ID())
		}
	}
}

// retired-versions keeps one title whatever retire_versions_apply_enabled says;
// "Preview only" is the plan_only badge, not a title suffix.
func TestRepairsLane_RetiredVersionsTitleIsFixed(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := &retiredVersionsFixer{applyEnabled: func() bool { return on }}
		require.Equal(t, "Retire trashed books into their survivor", f.Title(), "apply on = %v", on)
	}
}

// The owner retired three fixers on 2026-11-05: a trial of each found nothing
// to fix (or another fixer covers it). The plugin must not register them, so
// the lane does not list them and the engine refuses a start for their ids.
// Stored history under those ids is not rewritten, and the lane shows a link
// to one as "retired" (web useRepairsLane RETIRED_REPAIR_FIXER_IDS).
func TestRepairsLane_RetiredFixersAreNotRegistered(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	reg := p.Repairs()
	for _, id := range []string{
		"maintenance.normalize-letter-l-ordinals",
		"maintenance.relink-stale-series",
		"dedup.split-books",
	} {
		_, ok := reg.Get(id)
		require.False(t, ok, "%s is retired and must stay unregistered", id)
		for _, f := range reg.List() {
			require.NotEqual(t, id, f.ID())
		}
	}
}
