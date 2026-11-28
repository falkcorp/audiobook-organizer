// file: internal/server/invite_guest_access_identity_test.go
// version: 0.1.0
// guid: 41dc9fa1-e1db-4c60-a1a8-79ee36057a7f
// last-edited: 2026-11-22

package server

import (
	"errors"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// Under core identity the Cloudflare Access allow-list is the books key
// modules.books.cfaccess.allowed_emails, not the OIDC provider projection
// config.Snapshot().OAuthAllowedEmails. keepGuestPolicyEmail decides whether an
// address a person gave up may leave the Cloudflare guest policy, so it must ask
// the same list the admission policy asks: a removal judged against the wrong list
// cuts someone off. These fixtures give the two lists different values so the
// answer can only come from the one the branch is supposed to read.
func TestKeepGuestPolicyEmail_ReadsTheAccessListUnderIdentity(t *testing.T) {
	const (
		inSnapshot = "batman@example.test"
		inSeam     = "robin@example.test"
		owner      = "alfred@example.test"
		stranger   = "joker@example.test"
	)
	listed := func(v string) func() (string, error) { return func() (string, error) { return v, nil } }
	cases := []struct {
		name      string
		identity  bool
		allowList func() (string, error) // nil = server built without the option
		email     string
		wantKeep  string // "" = may be removed
	}{
		{"in the seam's list, not the snapshot's", true, listed(inSeam), inSeam, "it is on the Cloudflare Access allow-list"},
		{"in the seam's list among several, case and spacing differ", true, listed("x@example.test, ROBIN@example.test "), inSeam, "it is on the Cloudflare Access allow-list"},
		{"in the snapshot's list only: not the allow-list reason", true, listed(inSeam), inSnapshot, ""},
		{"seam list empty, snapshot lists it: removable", true, listed(""), inSnapshot, ""},
		{"on neither list", true, listed(inSeam), stranger, ""},
		{"owner is kept whatever the list says", true, listed(inSeam), owner, "it is the owner's email"},
		{"unreadable list keeps the entry", true, func() (string, error) { return "", errors.New("key not registered") }, stranger, "the Cloudflare Access allow-list could not be read, so it cannot be ruled out that this address is on it"},
		{"unreadable list keeps the entry even when a value came back with the error", true, func() (string, error) { return inSeam, errors.New("key not registered") }, inSeam, "the Cloudflare Access allow-list could not be read, so it cannot be ruled out that this address is on it"},
		{"no reader under identity keeps the entry", true, nil, stranger, "the Cloudflare Access allow-list cannot be read, so it cannot be ruled out that this address is on it"},
		// Off identity the read is HEAD's, byte for byte, and the seam is never asked.
		{"HEAD: the snapshot's list", false, nil, inSnapshot, "it is on OAUTH_ALLOWED_EMAILS"},
		{"HEAD: a seam without identity is ignored", false, listed(inSeam), inSeam, ""},
		{"HEAD: neither", false, nil, stranger, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved := config.Snapshot()
			t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = saved }) })
			store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/keep-guest.pebble")
			require.NoError(t, err)
			t.Cleanup(func() { store.Close() })
			opts := []ServerOption{}
			if tc.identity {
				id := adoptIdentity(t, nil, nil, nil)
				id.Users = guestUsersView{} // core's view, with no other account in it
				opts = append(opts, WithIdentity(id))
			}
			if tc.allowList != nil {
				opts = append(opts, WithAccessAllowList(tc.allowList))
			}
			srv := newTestServerWith(t, store, opts...)
			config.Mutate(func(c *config.Config) {
				c.OwnerEmail = owner
				c.OAuthAllowedEmails = inSnapshot
			})
			require.Equal(t, inSnapshot, config.Snapshot().OAuthAllowedEmails, "the fixture must put a different list in the snapshot")
			require.Equal(t, tc.wantKeep, srv.keepGuestPolicyEmail(t.Context(), "u-nobody", tc.email))
		})
	}
}
