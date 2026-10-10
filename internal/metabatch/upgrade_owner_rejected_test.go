// file: internal/metabatch/upgrade_owner_rejected_test.go
// version: 1.0.2
// guid: d28175fe-48ab-4f9e-8b21-994d29ad0bd0
// last-edited: 2026-10-10

package metabatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Synthetic stand-ins for the shared rank fixture's book and candidate, so
// these tests carry no real title or author.
const (
	syntheticTitle  = "Synthetic Test Title"
	syntheticAuthor = "Test Author"
)

func syntheticRankFixture() *rankFixture {
	f := newRankFixture()
	f.book.Title = syntheticTitle
	f.book.FilePath = "/library/" + syntheticAuthor + "/" + syntheticTitle + "/" + syntheticTitle + ".m4b"
	return f
}

func syntheticCandidate(source string, score float64) metafetch.MetadataCandidate {
	c := candidate(source, score)
	c.Title = syntheticTitle
	c.Author = syntheticAuthor
	return c
}

// The nightly upgrade searches fresh and REPLACES filled fields with the best
// gate-passing outranking candidate. A candidate the owner rejected must
// never be that candidate (regression, 2026-10-10).
func TestUpgrade_RefusesOwnerRejected(t *testing.T) {
	f := syntheticRankFixture()
	rej := syntheticCandidate("Audible", 0.95)
	key := "rejected_candidate:" + f.book.ID + ":" + rej.Source + "|" + rej.Title
	f.store.ScanPrefixFunc = func(prefix string) ([]database.KVPair, error) {
		if strings.HasPrefix(key, prefix) {
			return []database.KVPair{{Key: key, Value: []byte("1")}}, nil
		}
		return nil, nil
	}
	svc, fetcher := f.service(rej)
	out, err := svc.tryUpgradeBook(context.Background(), f.book.ID, "open_library")
	require.NoError(t, err)
	require.False(t, out.Upgraded, "owner-rejected candidate upgraded the book")
	require.Contains(t, out.Reason, "owner_rejected")
	require.Empty(t, fetcher.sources, "nothing applied")
	require.Nil(t, f.written(), "owner-rejected candidate overwrote a filled field")
}

// An unreadable rejection list fails the book closed: the upgrade cannot
// tell whether the owner rejected the candidate it would overwrite with.
func TestUpgrade_UnreadableRejectionsFailClosed(t *testing.T) {
	f := syntheticRankFixture()
	f.store.ScanPrefixFunc = func(string) ([]database.KVPair, error) {
		return nil, errors.New("scan failed")
	}
	svc, _ := f.service(syntheticCandidate("Audible", 0.95))
	out, err := svc.tryUpgradeBook(context.Background(), f.book.ID, "open_library")
	require.False(t, out.Upgraded)
	if err == nil {
		require.Contains(t, out.Reason, "owner_rejection_check_failed")
	}
	require.Nil(t, f.written())
}

// Control for the two tests above: with no rejection on record the same
// synthetic fixture and candidate DO upgrade the book, so the refusals there
// are the rejection check's doing and not a side effect of the synthetic data.
func TestUpgrade_SyntheticFixtureUpgradesWithoutRejection(t *testing.T) {
	f := syntheticRankFixture()
	f.store.ScanPrefixFunc = func(string) ([]database.KVPair, error) { return nil, nil }
	svc, _ := f.service(syntheticCandidate("Audible", 0.95))
	out, err := svc.tryUpgradeBook(context.Background(), f.book.ID, "open_library")
	require.NoError(t, err)
	require.True(t, out.Upgraded, "control did not upgrade: %s", out.Reason)
	require.NotNil(t, f.written())
}
