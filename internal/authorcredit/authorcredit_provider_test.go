// file: internal/authorcredit/authorcredit_provider_test.go
// version: 1.0.0
// guid: b2f6a9de-5c41-4d0e-8a73-1e9d4c7f3b60
// last-edited: 2026-10-13

package authorcredit

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
)

// providerOnly is a process-wide provider that offers only AuthoritySource.
type providerOnly struct{ lookup authority.Lookup }

func (p providerOnly) AuthorityLookup() authority.Lookup { return p.lookup }

// providerWaiter is a provider that also implements AuthorityWaiter, as the
// server's adapter does.
type providerWaiter struct {
	providerOnly
	ready bool
}

func (p providerWaiter) AwaitAuthorityLookup(context.Context) (authority.Lookup, bool) {
	return p.lookup, p.ready
}

// snapshotLookup builds a real snapshot holding one tier-O author.
func snapshotLookup(t *testing.T, name string) authority.Lookup {
	t.Helper()
	st := newStore(t)
	putPerson(t, st, authority.Person{Display: name, Roles: map[authority.Role]authority.RoleStat{
		authority.RoleAuthor: authorStat(authority.TierO, 1),
	}})
	return snapshot(t, st)
}

func TestAuthorityProvider_FallbackWhenStoreOffersNothing(t *testing.T) {
	st := newStore(t)
	_, ready := AwaitAuthority(context.Background(), st)
	require.False(t, ready, "no provider and no capability: not ready")

	l := snapshotLookup(t, "Provider Person")
	t.Cleanup(SetAuthorityProvider(providerOnly{lookup: l}))

	got, ready := AwaitAuthority(context.Background(), st)
	require.True(t, ready, "a plain store falls back to the provider")
	require.Same(t, l, got)
	got, ready = CurrentAuthority(st)
	require.True(t, ready)
	require.Same(t, l, got)
	require.Equal(t, EvidenceStrong, AuthorityPersonEvidence(got, "Provider Person").Strength)
}

func TestAuthorityProvider_WaiterIsWaitedOn(t *testing.T) {
	st := newStore(t)
	l := snapshotLookup(t, "Waited Person")

	rel := SetAuthorityProvider(providerWaiter{providerOnly: providerOnly{lookup: l}, ready: true})
	t.Cleanup(rel)
	got, ready := AwaitAuthority(context.Background(), st)
	require.True(t, ready)
	require.Same(t, l, got)

	rel()
	rel = SetAuthorityProvider(providerWaiter{providerOnly: providerOnly{lookup: authority.Empty()}, ready: false})
	t.Cleanup(rel)
	_, ready = AwaitAuthority(context.Background(), st)
	require.False(t, ready, "a waiter that has no snapshot yet is not ready")

	var nilSnap *authority.Snapshot
	rel()
	rel = SetAuthorityProvider(providerWaiter{providerOnly: providerOnly{lookup: nilSnap}, ready: true})
	t.Cleanup(rel)
	got, ready = AwaitAuthority(context.Background(), st)
	require.False(t, ready, "a provider answering a typed-nil snapshot is not ready")
	require.Equal(t, authority.Empty(), got)
}

// The store capability is consulted first: a store that offers the lists wins
// over the provider, and a store that offers the capability but answers
// nothing is not second-guessed by the provider.
func TestAuthorityProvider_StoreCapabilityWins(t *testing.T) {
	st := newStore(t)
	storeLookup := snapshotLookup(t, "Store Person")
	providerLookup := snapshotLookup(t, "Provider Person")
	t.Cleanup(SetAuthorityProvider(providerWaiter{providerOnly: providerOnly{lookup: providerLookup}, ready: true}))

	got, ready := AwaitAuthority(context.Background(), authorityLookupStore{PebbleStore: st, lookup: storeLookup})
	require.True(t, ready)
	require.Same(t, storeLookup, got, "AuthoritySource on the store beats the provider")

	got, ready = AwaitAuthority(context.Background(), authorityWaiterStore{PebbleStore: st, lookup: storeLookup, ready: true})
	require.True(t, ready)
	require.Same(t, storeLookup, got, "AuthorityWaiter on the store beats the provider")

	_, ready = AwaitAuthority(context.Background(), authorityLookupStore{PebbleStore: st, lookup: authority.Empty()})
	require.False(t, ready, "a store answering Empty is not overridden by the provider")
	require.Equal(t, authority.Empty(), authorityOf(authorityLookupStore{PebbleStore: st, lookup: authority.Empty()}))
}

func TestAuthorityProvider_ReleaseOnlyClearsItsOwnRegistration(t *testing.T) {
	st := newStore(t)
	first := snapshotLookup(t, "First")
	second := snapshotLookup(t, "Second")

	releaseFirst := SetAuthorityProvider(providerOnly{lookup: first})
	releaseSecond := SetAuthorityProvider(providerOnly{lookup: second})
	t.Cleanup(releaseSecond)

	releaseFirst() // an older server shutting down must not remove the newer provider
	got, ready := CurrentAuthority(st)
	require.True(t, ready)
	require.Same(t, second, got)

	releaseSecond()
	_, ready = CurrentAuthority(st)
	require.False(t, ready, "releasing the registered provider clears it")

	SetAuthorityProvider(providerOnly{lookup: first})
	SetAuthorityProvider(nil)
	_, ready = CurrentAuthority(st)
	require.False(t, ready, "a nil provider clears the registration")
}

// Registration and lookups race freely at server start and shutdown; run with
// -race.
func TestAuthorityProvider_ConcurrentSetAndLookup(t *testing.T) {
	st := newStore(t)
	l := snapshotLookup(t, "Racer")
	t.Cleanup(func() { SetAuthorityProvider(nil) })

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				rel := SetAuthorityProvider(providerWaiter{providerOnly: providerOnly{lookup: l}, ready: true})
				_, _ = AwaitAuthority(context.Background(), st)
				_, _ = CurrentAuthority(st)
				rel()
			}
		})
	}
	wg.Wait()
}
