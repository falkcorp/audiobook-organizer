// file: internal/plugins/maintenance/fragment_filesig_coarse_test.go
// version: 1.0.0
// guid: 2e6a9c14-7b30-4d85-bf12-c4d8e0a35f69
// last-edited: 2026-10-21

//go:build linux || darwin

package maintenance

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// coarseInfo is a stat of a file on a whole-second filesystem: the
// modification and status-change times are pinned to one whole second, so a
// same-size rewrite leaves the whole signature equal.
type coarseInfo struct{ os.FileInfo }

const coarseSec = 1_700_000_000

func (coarseInfo) ModTime() time.Time { return time.Unix(coarseSec, 0) }

func (c coarseInfo) Sys() any {
	st, ok := c.FileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	cp := *st
	setWholeSecondCtime(&cp, coarseSec)
	return &cp
}

func coarseStat(p string) (os.FileInfo, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	return coarseInfo{fi}, nil
}

func TestFragFileSig_CoarseTimestamps(t *testing.T) {
	t.Parallel()
	require.True(t, fragFileSig{MtimeNS: 5 * 1e9, CtimeNS: 7 * 1e9}.coarseTimestamps())
	require.False(t, fragFileSig{MtimeNS: 5*1e9 + 1, CtimeNS: 7 * 1e9}.coarseTimestamps())
	require.False(t, fragFileSig{MtimeNS: 5 * 1e9, CtimeNS: 7*1e9 + 3}.coarseTimestamps())
}

// N3: where timestamps are whole seconds a signature cannot prove a file
// unchanged, so the apply reads both files of a stored proof again; a
// same-size rewrite that leaves the signature equal is then caught by its
// digest.
func TestFragmentFixer_HashCopyGate_CoarseTimestampsAreRehashedAtApply(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T) (*fragFixture, *atomic.Int32) {
		t.Helper()
		f := hcSeed(t)
		f.hcChild(t, "c3", "Stray Recording", "hash-p03")
		fx := f.registeredFragFixer(t)
		fx.partialHashAbove = 1000
		fx.statFn = coarseStat
		reads := &atomic.Int32{}
		fx.wholeHashFn = func(ctx context.Context, p string) (fragFileSig, string, error) {
			reads.Add(1)
			_, sum, err := fragWholeHashFile(ctx, p)
			if err != nil {
				return fragFileSig{}, "", err
			}
			fi, err := coarseStat(p)
			if err != nil {
				return fragFileSig{}, "", err
			}
			sig, err := sigOf(fi)
			return sig, sum, err
		}
		b, err := os.ReadFile(f.path("lib/Alpha Omnibus/track-03.mp3"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(f.path("lib/Stray c3/stray-c3.mp3"), b, 0o644))
		return f, reads
	}

	t.Run("unchanged files: the proof is read again and holds", func(t *testing.T) {
		t.Parallel()
		f, reads := seed(t)
		r := hcOnlyRow(t, f.plan(t, "op-plan"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, int32(2), reads.Load())
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		require.Greater(t, reads.Load(), int32(2), "a whole-second signature is not trusted: both files are read again")
	})

	t.Run("same-size rewrite with an equal signature: refused", func(t *testing.T) {
		t.Parallel()
		f, _ := seed(t)
		r := hcOnlyRow(t, f.plan(t, "op-plan"))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.NoError(t, os.WriteFile(f.path("lib/Stray c3/stray-c3.mp3"), fragFixtureBytes("rewritten", 1003), 0o644))
		out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
		require.Zero(t, out.Applied, "%+v", out.Rows)
		require.True(t, f.live(t, "c3"))
	})
}
