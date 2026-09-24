// file: internal/plugins/maintenance/fs_regroup_primary_handoff_test.go
// version: 1.0.0
// guid: 7c3a9e15-4d2b-4f80-a6e1-b95d0c28f473
// last-edited: 2026-09-24

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func fsLivePrimaries(t *testing.T, s *database.PebbleStore, gid string) []string {
	t.Helper()
	members, err := s.GetBooksByVersionGroup(gid)
	require.NoError(t, err)
	var out []string
	for i := range members {
		m := &members[i]
		if !m.IsSoftDeleted() && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			out = append(out, m.ID)
		}
	}
	return out
}

// A retired shell that was its version group's primary hands the flag to the
// group's library copy; reverting the operation re-crowns the shell and
// demotes the copy, so the group has one primary on both sides.
func TestFsRegroupDuplicates_RetiredPrimaryShellHandsOnAndRevertRecrowns(t *testing.T) {
	s := regroupStore(t)
	root := t.TempDir()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	// The planner retires shells only in the owner's version group, so the
	// owner (nil flag, not in the library) shares it with the primary shell
	// and an organized library copy.
	owner, shells := seedDuplicates(t, s, "/lib/Kevin J. Anderson/Metal Swarm")
	vg := "vg-shell"
	yes, no := true, false
	for id, flag := range map[string]*bool{shells[0]: &yes, owner: nil} {
		_, err := s.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID, b.IsPrimaryVersion = &vg, flag
			return nil
		})
		require.NoError(t, err)
	}

	libPath := filepath.Join(root, "Kevin J. Anderson", "Metal Swarm", "ms.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(libPath), 0o755))
	require.NoError(t, os.WriteFile(libPath, []byte("m4b"), 0o644))
	organized := "organized"
	lib, err := s.CreateBook(&database.Book{Title: "Metal Swarm (library)", FilePath: libPath, LibraryState: &organized, VersionGroupID: &vg, IsPrimaryVersion: &no})
	require.NoError(t, err)
	_, err = s.ModifyBook(lib.ID, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "bf-lib", BookID: lib.ID, FilePath: libPath}))

	plan := fsPlan(t, s)
	require.Len(t, fsGroupsOf(plan, fsCatDuplicates), 1, plan.summary())
	_, err = applyFSRepairPlan(context.Background(), &fsGuardStore{PebbleStore: s}, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-vg"})
	require.NoError(t, err)
	require.True(t, fsSoftDeleted(t, s, shells[0]))
	require.Equal(t, []string{lib.ID}, fsLivePrimaries(t, s, vg))

	res, err := audiobooks.NewRevertService(s).RevertOperation("op-vg")
	require.NoError(t, err, "%+v", res)
	require.False(t, fsSoftDeleted(t, s, shells[0]))
	require.Equal(t, []string{shells[0]}, fsLivePrimaries(t, s, vg))
}
