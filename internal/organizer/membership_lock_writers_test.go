// file: internal/organizer/membership_lock_writers_test.go
// version: 1.2.0
// guid: 9c4f7a26-3b8e-4d19-a6c5-0e2d8b1f7a43
// last-edited: 2026-10-02

package organizer

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// CreateOrganizedVersion moves the original into the new version group, so
// it waits for a concurrent hand-off on the original's group and, for an
// ungrouped original, on the no-group sentinel.
func TestCreateOrganizedVersion_WaitsForGroupHolder(t *testing.T) {
	cases := []struct{ name, group, held string }{
		{"grouped original", "g", "g"},
		{"ungrouped original", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := vptest.New(t)
			orig := f.Book(t, vptest.Spec{ID: "orig", Group: tc.group, Primary: "true", State: "imported"})
			b, err := f.S.GetBookByID(orig)
			require.NoError(t, err)
			svc := NewService(f.S)
			landing := &Landing{Path: filepath.Join(t.TempDir(), "organized-orig.m4b")}
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(tc.held) },
				func() error {
					_, err := svc.CreateOrganizedVersion(b, landing, "", &noopLogger{})
					return err
				},
				func() bool { return f.GroupOf(t, orig) == tc.group })
		})
	}
}

// adoptIntoOccupantGroup moves two ungrouped books into a minted group, so
// the in-place organize that adopts waits for the no-group sentinel.
func TestInPlaceAdopt_WaitsForNoGroupSentinel(t *testing.T) {
	svc, store, root := setupInPlace(t)
	audio := filled(4096, 0xA1)
	src := filepath.Join(root, "incoming", "x.mp3")
	b := addInPlaceBook(t, store, "book-b", "Title", src, audio, nil, 0)
	target := targetFor(t, svc, b)
	addInPlaceBook(t, store, "book-o", "Title", target, audio, nil, 0)

	var landing *Landing
	vptest.RequireWaitsForHolder(t,
		func() func() { return versionprimary.LockGroup("") },
		func() error {
			var err error
			landing, err = svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
			return err
		},
		func() bool { g := getInPlaceBook(t, store, b.ID).VersionGroupID; return g == nil || *g == "" })
	require.NotNil(t, landing.Resolution)
	require.Equal(t, OutcomeAdopted, landing.Resolution.Outcome)
}

// CreateOrganizedVersion planned versionGroupID from `book` as the caller
// read it. An original that changed group since is refused under the locks:
// it is not moved out of a group nobody locked into the stale one.
func TestCreateOrganizedVersion_RefusesOriginalMovedAfterRead(t *testing.T) {
	f := vptest.New(t)
	f.Book(t, vptest.Spec{ID: "h-inc", Group: "h", Primary: "true"})
	orig := f.Book(t, vptest.Spec{ID: "orig", Group: "g", Primary: "true", State: "imported"})
	snapshot, err := f.S.GetBookByID(orig)
	require.NoError(t, err)
	_, err = f.S.ModifyBook(orig, func(b *database.Book) error {
		h, no := "h", false
		b.VersionGroupID, b.IsPrimaryVersion = &h, &no
		return nil
	})
	require.NoError(t, err)

	landing := &Landing{Path: filepath.Join(t.TempDir(), "organized-orig.m4b")}
	created, err := NewService(f.S).CreateOrganizedVersion(snapshot, landing, "", &noopLogger{})
	require.ErrorIs(t, err, versionprimary.ErrMembershipChanged)
	require.Nil(t, created)
	require.Equal(t, "h", f.GroupOf(t, orig), "the original stays in the group it moved to")
	// No copy was created, so none is linked into the stale group g, and h
	// holds only its own members.
	inG, err := f.S.GetBooksByVersionGroup("g")
	require.NoError(t, err)
	require.Empty(t, inG, "no organized copy may join the stale group")
	inH, err := f.S.GetBooksByVersionGroup("h")
	require.NoError(t, err)
	require.Len(t, inH, 2, "h holds h-inc and the original only")
	require.Equal(t, "g", *snapshot.VersionGroupID, "the caller's book is not rewritten on a refusal")
	require.True(t, *snapshot.IsPrimaryVersion)
}
