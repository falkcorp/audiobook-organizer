//go:build embed_frontend

// file: internal/books/identity_old_links_fallback_embed_test.go
// version: 0.1.0
// guid: 9cf7a250-a1f3-46d4-a83f-3d8e01cda526
// last-edited: 2026-11-27

package books

import "net/http"

// The page fallback of a build that embeds the SPA (internal/server
// static_embed.go): a navigation path no route matches is answered with the SPA
// document itself, 200 and no Location. TestOldLinkFormsNotServed holds an old
// /invite/<token> link to it.
const (
	oldInviteFallbackStatus   = http.StatusOK
	oldInviteFallbackLocation = ""
)
