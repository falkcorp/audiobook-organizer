// file: internal/server/transcode_version_lock_test.go
// version: 1.2.0
// guid: 5d8c2b47-9a1e-4f36-8b70-e4a2c9f1d053
// last-edited: 2026-10-02

package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// recordTranscodedVersion's group write waits for a concurrent hand-off on
// the original's group, and, for an ungrouped original, on the no-group
// sentinel it leaves.
func TestRecordTranscodedVersion_WaitsForGroupHolder(t *testing.T) {
	cases := []struct{ name, group, held string }{
		{"grouped original", "g", "g"},
		{"ungrouped original", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := vptest.New(t)
			orig := f.Book(t, vptest.Spec{ID: "orig", Group: tc.group, Primary: "true"})
			b, err := f.S.GetBookByID(orig)
			require.NoError(t, err)
			out := transcodeOutput(t, f.Root, "orig-out")
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(tc.held) },
				func() error {
					_, _, err := recordTranscodedVersion(context.Background(), f.S, b, out, 128, f.Root, noLog)
					return err
				},
				func() bool { return f.GroupOf(t, orig) == tc.group })
		})
	}
}

// The original's group is read before the (long) transcode. When the
// original changes group in the meantime, the locked write is refused and
// re-planned from a fresh read: the original stays in its new group and the
// M4B version joins that group, not the stale one.
func TestRecordTranscodedVersion_ReplansWhenOriginalMoved(t *testing.T) {
	f := vptest.New(t)
	f.Book(t, vptest.Spec{ID: "g-inc", Group: "g", Primary: "true"})
	f.Book(t, vptest.Spec{ID: "h-inc", Group: "h", Primary: "true"})
	orig := f.Book(t, vptest.Spec{ID: "orig", Group: "g", Primary: "false"})
	snapshot, err := f.S.GetBookByID(orig)
	require.NoError(t, err)
	_, err = f.S.ModifyBook(orig, func(b *database.Book) error {
		h := "h"
		b.VersionGroupID = &h
		return nil
	})
	require.NoError(t, err)

	nb, _, err := recordTranscodedVersion(context.Background(), f.S, snapshot,
		transcodeOutput(t, f.Root, "orig-out"), 128, f.Root, noLog)
	require.NoError(t, err)
	require.Equal(t, "h", f.GroupOf(t, orig))
	require.NotNil(t, nb)
	require.Equal(t, "h", f.GroupOf(t, nb.ID), "the M4B joins the original's current group")
}

// flipsOnEveryRead moves the original to a fresh group on every read, so
// every re-plan is stale: after three attempts the M4B record must be created
// ungrouped, never linked into a stale group.
type flipsOnEveryRead struct {
	*database.PebbleStore
	id string
	n  int
}

func (s *flipsOnEveryRead) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if id == s.id && err == nil {
		s.n++
		g := fmt.Sprintf("moving-%d", s.n)
		if _, merr := s.PebbleStore.ModifyBook(id, func(r *database.Book) error {
			r.VersionGroupID = &g
			return nil
		}); merr != nil {
			return nil, merr
		}
	}
	return b, err
}

func TestRecordTranscodedVersion_GivesUpUngroupedAfterThreeStalePlans(t *testing.T) {
	f := vptest.New(t)
	orig := f.Book(t, vptest.Spec{ID: "orig", Group: "g", Primary: "true"})
	snapshot, err := f.S.GetBookByID(orig)
	require.NoError(t, err)
	st := &flipsOnEveryRead{PebbleStore: f.S, id: orig}
	nb, _, err := recordTranscodedVersion(context.Background(), st, snapshot,
		transcodeOutput(t, f.Root, "orig-out"), 128, f.Root, noLog)
	require.NoError(t, err)
	require.NotNil(t, nb)
	require.Equal(t, "", f.GroupOf(t, nb.ID), "the M4B record must not join a stale group")
	require.Equal(t, "nil", f.Flag(t, nb.ID))
	require.NotEqual(t, "g", f.GroupOf(t, orig))
}
