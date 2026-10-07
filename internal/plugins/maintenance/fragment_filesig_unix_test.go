// file: internal/plugins/maintenance/fragment_filesig_unix_test.go
// version: 1.0.0
// guid: 8e3c6a19-2f7b-4d05-b9a4-1c6e8f3d7a52
// last-edited: 2026-10-06

//go:build linux || darwin

package maintenance

import (
	"context"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// zeroInodeInfo is a stat whose platform data reports inode 0, as some FUSE
// and SMB mounts do for every file.
type zeroInodeInfo struct{ os.FileInfo }

func (zeroInodeInfo) Sys() any { return &syscall.Stat_t{} }

// TestFragmentFixer_ZeroInodeNeverProves (review round 2): a filesystem that
// reports inode 0 cannot tell a path alias from a copy, so no proof is made
// from it and a stored proof is never restored through it.
func TestFragmentFixer_ZeroInodeNeverProves(t *testing.T) {
	t.Parallel()

	t.Run("sigOf refuses inode 0", func(t *testing.T) {
		t.Parallel()
		p := t.TempDir() + "/x.mp3"
		require.NoError(t, os.WriteFile(p, []byte("abc"), 0o644))
		fi, err := os.Stat(p)
		require.NoError(t, err)
		_, err = sigOf(zeroInodeInfo{fi})
		require.ErrorContains(t, err, "inode 0")
	})

	t.Run("a claimant read with no inode stays copy-unproven", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
		// libA is a hardlink of the parent's file on a mount with no inodes:
		// the alias check cannot see it, so it must not be proven.
		require.NoError(t, os.Remove(f.path(hpLibA)))
		require.NoError(t, os.Link(f.path(hpParent), f.path(hpLibA)))
		bad := f.path(hpLibA)
		f.registeredFragFixer(t).hashFn = func(p string) (fragFileSig, string, error) {
			sig, sum, err := fragHashFile(p)
			if p == bad {
				sig.Dev, sig.Ino = 0, 0
			}
			return sig, sum, err
		}
		res := f.plan(t, "op-plan")
		r := findRow(t, res, fragRowCopyUnproven+":"+f.ids["parent"])
		require.Equal(t, fragSkipCopyUnproven, r.Skipped)
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libA"]}, r.BookIDs)
		require.Contains(t, r.SkipReason, "identity (device and inode) is unknown")
		proven := findRow(t, res, "copy:"+f.ids["parent"])
		require.ElementsMatch(t, []string{f.ids["parent"], f.ids["libB"]}, proven.BookIDs)
	})

	t.Run("the re-plan refuses a proof once the file reports inode 0", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		f.writeSame(t, "same", hpParent, hpLibA, hpLibB)
		planned := findRow(t, f.plan(t, "op-plan"), "copy:"+f.ids["parent"])
		fx := newFragmentFixer(f.p)
		fx.statFn = func(p string) (os.FileInfo, error) {
			fi, err := os.Stat(p)
			if err != nil || p != f.path(hpLibB) {
				return fi, err
			}
			return zeroInodeInfo{fi}, nil
		}
		got, err := fx.Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		require.NotEqual(t, planned.Fingerprint, got.Fingerprint)
		require.Contains(t, got.Reason, "inode 0")
	})

	t.Run("restoreContentProofs refuses a stored proof with no inode", func(t *testing.T) {
		t.Parallel()
		f := copyClaimantsFixture(t, false)
		f.writeSame(t, "same", hpParent, hpLibA)
		fx := newFragmentFixer(f.p)
		a, pa := f.path(hpLibA), f.path(hpParent)
		fs, _, err := fragHashFile(a)
		require.NoError(t, err)
		ps, _, err := fragHashFile(pa)
		require.NoError(t, err)
		fs.Dev, fs.Ino = 0, 0
		why := fx.restoreContentProofs(newFragLibrary(), []fragContentProof{{FragBook: "b", FragFile: "f", FragPath: a, FragSig: fs,
			FragDigest: "d", ParentRow: "r", ParentPath: pa, ParentSig: ps, ParentDigest: "d"}})
		require.Contains(t, why, "no file identity")
	})
}
