//go:build !embed_frontend

// file: internal/books/identity_old_links_fallback_nonembed_test.go
// version: 0.1.0
// guid: f00526c9-be27-476f-aef5-fe8c99add820
// last-edited: 2026-11-27

package books

import "net/http"

// The page fallback of a build without the embedded SPA, which is how CI runs
// the tests (internal/server static_nonembed.go): a navigation path no route
// matches is sent to the placeholder home page with 302 Location "/". The
// Location is the bare root, so it carries no token and opens no invite.
// TestOldLinkFormsNotServed holds an old /invite/<token> link to it.
const (
	oldInviteFallbackStatus   = http.StatusFound
	oldInviteFallbackLocation = "/"
)
