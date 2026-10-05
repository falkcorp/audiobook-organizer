// file: internal/server/authority_evidence_test.go
// version: 1.0.0
// guid: 4492804b-1186-4576-8833-2d1d7a405363
// last-edited: 2026-10-05

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// waitAuthorityLoad waits for the in-flight snapshot load, if any.
func waitAuthorityLoad(t *testing.T, a *authorityEvidence) {
	t.Helper()
	a.mu.Lock()
	done := a.loadDone
	a.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("authority snapshot load did not finish")
	}
}

// The production store (Server.store, the indexedStore decorator) offers
// authorcredit.AuthoritySource. With authority_evidence_enabled off it
// answers authority.Empty(); turned on (no restart) the first call starts a
// background load and answers Empty until it lands, then the snapshot. A
// credit resolved through it then puts a tier-O author named
// like a series first.
func TestServerStore_AuthorityEvidenceCapability(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	store := server.store
	src, ok := database.AsCapability[authorcredit.AuthoritySource](store)
	require.True(t, ok, "the server store must offer authorcredit.AuthoritySource")

	for _, n := range []string{"Michael Anderle", "Craig Martelle"} {
		_, err := store.CreateAuthor(n)
		require.NoError(t, err)
	}
	ma, err := store.GetAuthorByName("Michael Anderle")
	require.NoError(t, err)
	_, err = store.CreateSeries("Michael Anderle", &ma.ID)
	require.NoError(t, err)
	require.NoError(t, authority.PutPersonOverride(store, authority.PersonOverride{Name: "Michael Anderle",
		Roles: map[authority.Role]bool{authority.RoleAuthor: true}, SetAt: time.Now()}))
	authorcredit.ResetTitleCache()
	t.Cleanup(authorcredit.ResetTitleCache)

	require.False(t, config.AppConfig.AuthorityEvidenceEnabled)
	require.False(t, src.AuthorityLookup().IsKnownPerson("Michael Anderle", authority.RoleAuthor), "flag off: Empty")
	got, err := authorcredit.Resolve(store, "Michael Anderle, Craig Martelle", authorcredit.PrepareGate)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Craig Martelle", got[0].Name, "flag off: no evidence, the series-named part is dropped")

	config.AppConfig.AuthorityEvidenceEnabled = true
	ae := store.(*indexedStore).authority
	_ = src.AuthorityLookup() // starts the background load
	waitAuthorityLoad(t, ae)
	require.True(t, src.AuthorityLookup().IsKnownPerson("Michael Anderle", authority.RoleAuthor), "flag on: the snapshot")
	got, err = authorcredit.Resolve(store, "Michael Anderle, Craig Martelle", authorcredit.PrepareGate)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, []string{"Michael Anderle", "Craig Martelle"}, []string{got[0].Name, got[1].Name})
}

// A failed load answers Empty (no snapshot yet) and backs off instead of
// retrying on every call.
func TestAuthorityEvidence_LoadFailureAnswersEmpty(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // LoadSnapshot fails on the cancelled context
	a := newAuthorityEvidence(ctx, inner, func() bool { return true })
	require.Nil(t, a.Lookup().Person("anyone"))
	waitAuthorityLoad(t, a)
	a.mu.Lock()
	next, snap := a.nextLoad, a.snap
	a.mu.Unlock()
	require.Nil(t, snap)
	require.True(t, next.After(time.Now()), "a failed load backs off")
	var nilA *authorityEvidence
	require.Nil(t, nilA.Lookup().Person("anyone"), "a nil source answers Empty")
}
