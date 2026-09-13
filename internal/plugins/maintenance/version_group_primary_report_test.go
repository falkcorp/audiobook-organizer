// file: internal/plugins/maintenance/version_group_primary_report_test.go
// version: 1.0.0
// guid: 7c2e9a41-5d8f-4b36-a1e0-8f3b6d2c9e57
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

func vgPrimaryFixture() []database.BookCore {
	tr, f := true, false
	g := func(s string) *string { return &s }
	return []database.BookCore{
		// G-EXPLICIT: two stored trues -> double, explicit.
		{ID: "A1", VersionGroupID: g("G-EXPLICIT"), IsPrimaryVersion: &tr},
		{ID: "A2", VersionGroupID: g("G-EXPLICIT"), IsPrimaryVersion: &tr},
		// G-NIL: one true + one nil -> double only because nil reads as primary.
		{ID: "B1", VersionGroupID: g("G-NIL"), IsPrimaryVersion: &tr},
		{ID: "B2", VersionGroupID: g("G-NIL")},
		// G-OK: one true + one false -> fine.
		{ID: "C1", VersionGroupID: g("G-OK"), IsPrimaryVersion: &tr},
		{ID: "C2", VersionGroupID: g("G-OK"), IsPrimaryVersion: &f},
		// G-ZERO: every member false -> zero-primary.
		{ID: "D1", VersionGroupID: g("G-ZERO"), IsPrimaryVersion: &f},
		{ID: "D2", VersionGroupID: g("G-ZERO"), IsPrimaryVersion: &f},
		// G-TRASH: second true is soft-deleted -> not live, group is fine.
		{ID: "E1", VersionGroupID: g("G-TRASH"), IsPrimaryVersion: &tr},
		{ID: "E2", VersionGroupID: g("G-TRASH"), IsPrimaryVersion: &tr, MarkedForDeletion: &tr},
		// Ungrouped books never count.
		{ID: "U1", IsPrimaryVersion: &tr},
		{ID: "U2"},
	}
}

func TestVersionGroupPrimaryReport_ClassifiesGroups(t *testing.T) {
	r := findVersionGroupPrimaryViolations(vgPrimaryFixture(), 100)
	require.Equal(t, 12, r.TotalBooks)
	require.Equal(t, 1, r.SoftDeleted)
	require.Equal(t, 5, r.Groups)
	require.Equal(t, 2, r.DoubleGroups)
	require.Equal(t, 1, r.DoubleExplicit)
	require.Equal(t, 1, r.DoubleNilOnly)
	require.Equal(t, 4, r.BooksInDoubles)
	require.Equal(t, 1, r.ZeroPrimary)
	require.Len(t, r.Sample, 2)
	require.Equal(t, "G-EXPLICIT", r.Sample[0].GroupID)
	require.Equal(t, "G-NIL", r.Sample[1].GroupID)
	require.Equal(t, []vgPrimaryMember{{ID: "B1", Stored: "true"}, {ID: "B2", Stored: "nil"}}, r.Sample[1].Members)
}

func TestVersionGroupPrimaryReport_SampleLimit(t *testing.T) {
	r := findVersionGroupPrimaryViolations(vgPrimaryFixture(), 1)
	require.Equal(t, 2, r.DoubleGroups, "the count must not be capped by the sample limit")
	require.Len(t, r.Sample, 1)
}

// TestVersionGroupPrimaryReport_FindsSeededDoubleInRealStore seeds the prod
// shape through the real store and runs the op end to end: a report that only
// works on hand-built BookCore slices proves nothing about what the store
// actually hands back.
func TestVersionGroupPrimaryReport_FindsSeededDoubleInRealStore(t *testing.T) {
	store := newApplyTestStore(t)
	groupID := ulid.Make().String()
	var ids []string
	for _, title := range []string{"Double A", "Double B"} {
		id := ulid.Make().String()
		_, err := store.CreateBook(&database.Book{ID: id, Title: title, Format: "mp3", FilePath: "/lib/vgp/" + title + ".mp3"})
		require.NoError(t, err)
		putInGroup(t, store, id, groupID, true)
		ids = append(ids, id)
	}

	p := &Plugin{deps: &fakeDeps{store: store}}
	rep := &fakeReporter{}
	require.NoError(t, p.runVersionGroupPrimaryReport(context.Background(), nil, rep))
	all := strings.Join(rep.logs, "\n")
	require.Contains(t, all, "REPORT ONLY (nothing changed)")
	require.Contains(t, all, "double_primary_groups=1 (explicit=1 nil_only=0)")
	require.Contains(t, all, "double-primary group "+groupID)

	// Report only: both rows still carry their flags.
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b.IsPrimaryVersion)
		require.True(t, *b.IsPrimaryVersion, "report op must not repair anything")
	}
}

func TestVersionGroupPrimaryReport_WritesNothing(t *testing.T) {
	writes := 0
	store := &database.MockStore{
		GetAllBooksCoreCompleteFunc: func(limit, offset int) ([]database.BookCore, error) {
			if limit != 0 || offset != 0 {
				t.Fatalf("report must use a single limit-0 read, got limit=%d offset=%d", limit, offset)
			}
			return vgPrimaryFixture(), nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			writes++
			return b, nil
		},
	}
	p := &Plugin{deps: &fakeDeps{store: store}}
	require.NoError(t, p.runVersionGroupPrimaryReport(context.Background(), nil, &fakeReporter{}))
	require.Zero(t, writes, "report-only op attempted writes")
}

func TestVersionGroupPrimaryReport_DefIsManualReadOnlyAndRegistered(t *testing.T) {
	def := (&Plugin{}).versionGroupPrimaryReportDef()
	require.Nil(t, def.Schedule, "report op must have no schedule")
	require.True(t, reflect.DeepEqual(def.Capabilities, []sdk.Capability{sdk.CapLibraryRead}))
	require.Equal(t, sdk.LivenessManual, def.Liveness)

	reg := &phantomCaptureRegistry{}
	require.NoError(t, New(fakeDeps{}).Register(reg))
	found := 0
	for _, id := range reg.ids {
		if id == "maintenance.version-group-primary-report" {
			found++
		}
	}
	require.Equal(t, 1, found)
}
