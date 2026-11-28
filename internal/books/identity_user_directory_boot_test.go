// file: internal/books/identity_user_directory_boot_test.go
// version: 1.0.0
// guid: 6f1d07a9-61f7-4781-bd07-a2c3f140e101
// last-edited: 2026-11-28

package books

import (
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// The store's account list after the flipped boot (F4-08 fix-forward,
// data-loss prevention). The HEAD store on disk holds three users and one of
// them has listening progress; the flipped boot's M3 imports the users into
// core and drains u:. The store every holder reads (the module's, the one the
// server, the merge service and the maintenance jobs are handed) must then list
// core's accounts, and the "keep the book if anyone has state" probe must see
// the progress, or every merge and every hard-delete gate on a migrated install
// proceeds as if nobody had listened to anything.
func TestIdentityBoot_StoreUserListIsCoresDirectory(t *testing.T) {
	seed := &fpSeed{}
	fx := newFlipFixture(t, func(s *database.PebbleStore) { seedFootprint(t, s, seed) }, "")
	r := bootOverHEADStore(t, fx)
	st := r.mod.store.Load()
	require.NotNil(t, st)

	raw, err := st.GetRaw("u:" + seed.target)
	require.NoError(t, err)
	require.Nil(t, raw, "precondition: M3 drained the HEAD user row")

	for name, list := range map[string]func() ([]database.User, error){"ListUsers": st.ListUsers, "ListUsersStrict": st.ListUsersStrict} {
		users, err := list()
		require.NoError(t, err, name)
		ids := map[string]string{}
		for _, u := range users {
			ids[u.ID] = u.Status
		}
		for _, id := range []string{seed.admin, seed.viewer, seed.target} {
			require.Equal(t, "active", ids[id], "%s lists the migrated account %s from core: %v", name, id, ids)
		}
	}

	has, err := merge.BookHasUserProgress(st, "book-gotham-1")
	require.NoError(t, err)
	require.True(t, has, "the migrated user's progress is seen through core's directory")
	probe, err := merge.NewUserStateProbe(st)
	require.NoError(t, err)
	has, err = probe.Has("book-gotham-2")
	require.NoError(t, err)
	require.True(t, has, "the hard-delete probe keeps a book a migrated user has state on")
}
