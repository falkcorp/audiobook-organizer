// file: internal/plugins/maintenance/fs_regroup_primary_handoff_test.go
// version: 1.2.0
// guid: 7c3a9e15-4d2b-4f80-a6e1-b95d0c28f473
// last-edited: 2026-10-07

package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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

// The retired shell's hand-off would demote an iTunes member of the group
// (nil flag): the iTunes guard refuses it, nothing is written, the refusal is
// noted in the journal and counted, and the apply carries on.
func TestFsRegroupDuplicates_HandOffNeverWritesAnITunesMember(t *testing.T) {
	s := regroupStore(t)
	root := t.TempDir()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	owner, shells := seedDuplicates(t, s, "/lib/Ada Quill/Metal Swarm") // the helper names the folder
	vg := "vg-shell-it"
	yes, no := true, false
	for id, flag := range map[string]*bool{shells[0]: &yes, owner: nil} {
		_, err := s.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID, b.IsPrimaryVersion = &vg, flag
			return nil
		})
		require.NoError(t, err)
	}
	libPath := filepath.Join(root, "Ada Quill", "Splashdown Tides", "st.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(libPath), 0o755))
	require.NoError(t, os.WriteFile(libPath, []byte("m4b"), 0o644))
	organized := "organized"
	lib, err := s.CreateBook(&database.Book{Title: "Splashdown Tides (library)", FilePath: libPath, LibraryState: &organized, VersionGroupID: &vg})
	require.NoError(t, err)
	_, err = s.ModifyBook(lib.ID, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "bf-lib-it", BookID: lib.ID, FilePath: libPath}))
	pid := "PID-SYNTH-1"
	it, err := s.CreateBook(&database.Book{Title: "Splashdown Tides (iTunes)", FilePath: "/elsewhere/st-itunes.m4b", VersionGroupID: &vg, ITunesPersistentID: &pid})
	require.NoError(t, err)

	plan := fsPlan(t, s)
	require.Len(t, fsGroupsOf(plan, fsCatDuplicates), 1, plan.summary())
	res, err := applyFSRepairPlan(context.Background(), &fsGuardStore{PebbleStore: s}, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-vg-it"})
	require.NoError(t, err)
	require.Equal(t, 1, res.HandOffsRefused, res.String())
	require.Equal(t, 0, res.Errors, res.String())
	require.True(t, fsSoftDeleted(t, s, shells[0]))

	got, err := s.GetBookByID(it.ID)
	require.NoError(t, err)
	require.Nil(t, got.IsPrimaryVersion, "the iTunes member's flag is never written")
	gl, err := s.GetBookByID(lib.ID)
	require.NoError(t, err)
	require.Equal(t, &no, gl.IsPrimaryVersion, "nothing crowned")

	changes, err := s.GetOperationChanges("op-vg-it")
	require.NoError(t, err)
	var refused, handOff int
	for _, c := range changes {
		switch c.ChangeType {
		case undo.ChangeTypeBookPrimaryHandoffRefused:
			refused++
		case undo.ChangeTypeBookPrimaryHandoff:
			handOff++
		}
	}
	require.Equal(t, 1, refused)
	require.Zero(t, handOff)
}

// extIDErrStore fails GetExternalIDsForBook for one book: the iTunes
// guard's read of that member fails (a failure, not a refusal).
type extIDErrStore struct {
	*fsGuardStore
	failFor string
}

func (s *extIDErrStore) GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error) {
	if bookID == s.failFor {
		return nil, errors.New("synthetic external-id read failure")
	}
	return s.fsGuardStore.GetExternalIDsForBook(bookID)
}

// Round 2: the retired shell's hand-off fails before any write (the guard's
// read of a member it would demote fails). It wrote nothing, so it is noted
// like a refusal: the revert of the regroup leaves the library copy K,
// whose explicit true predates the op, primary instead of crowning the
// shell back over it. Before the fix no note was written and the revert
// demoted K.
func TestFsRegroupDuplicates_HandOffFailedBeforeAnyWriteKeepsTheIncumbentOnRevert(t *testing.T) {
	s := regroupStore(t)
	root := t.TempDir()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	owner, shells := seedDuplicates(t, s, "/lib/Ada Quill/Metal Swarm")
	vg := "vg-shell-readerr"
	yes := true
	for id, flag := range map[string]*bool{shells[0]: &yes, owner: nil} {
		_, err := s.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID, b.IsPrimaryVersion = &vg, flag
			return nil
		})
		require.NoError(t, err)
	}
	libPath := filepath.Join(root, "Ada Quill", "Metal Swarm", "ms.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(libPath), 0o755))
	require.NoError(t, os.WriteFile(libPath, []byte("m4b"), 0o644))
	organized := "organized"
	k, err := s.CreateBook(&database.Book{Title: "Metal Swarm (library)", FilePath: libPath, LibraryState: &organized, VersionGroupID: &vg})
	require.NoError(t, err)
	_, err = s.ModifyBook(k.ID, func(b *database.Book) error { b.IsPrimaryVersion = &yes; return nil })
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "bf-lib-readerr", BookID: k.ID, FilePath: libPath}))

	plan := fsPlan(t, s)
	require.Len(t, fsGroupsOf(plan, fsCatDuplicates), 1, plan.summary())
	st := &extIDErrStore{fsGuardStore: &fsGuardStore{PebbleStore: s}, failFor: owner}
	res, _ := applyFSRepairPlan(context.Background(), st, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-vg-readerr"})
	require.Zero(t, res.HandOffsRefused, res.String())
	require.Equal(t, 1, res.Errors, res.String())
	require.True(t, fsSoftDeleted(t, s, shells[0]))
	ob, err := s.GetBookByID(owner)
	require.NoError(t, err)
	require.Nil(t, ob.IsPrimaryVersion, "nothing was written")

	changes, err := s.GetOperationChanges("op-vg-readerr")
	require.NoError(t, err)
	noted := false
	for _, c := range changes {
		noted = noted || (c.BookID == shells[0] && c.ChangeType == undo.ChangeTypeBookPrimaryHandoffRefused)
	}
	require.True(t, noted, "a hand-off that wrote nothing is noted for the revert")

	_, err = audiobooks.NewRevertService(s).RevertOperation("op-vg-readerr")
	require.NoError(t, err)
	kb, err := s.GetBookByID(k.ID)
	require.NoError(t, err)
	require.NotNil(t, kb.IsPrimaryVersion)
	require.True(t, *kb.IsPrimaryVersion, "the incumbent the op never wrote keeps its flag")
	sb, err := s.GetBookByID(shells[0])
	require.NoError(t, err)
	require.NotNil(t, sb.IsPrimaryVersion)
	require.False(t, *sb.IsPrimaryVersion, "the shell comes back non-primary beside the incumbent")
}
