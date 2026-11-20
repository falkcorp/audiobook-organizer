//go:build embed_frontend

// file: core/examples/hello/ui_embed.go
// version: 0.1.0
// guid: 1e7c4a09-5b3d-4f82-9a61-c8d2f0b7e354
// last-edited: 2026-11-20

package hello

import (
	"embed"

	"github.com/falkcorp/mediacore/ui"
)

// web/dist is a sibling of this file, so the pattern needs no ".." (MOD-20).
// go:embed accepts only a package-level variable, and an embed.FS is
// read-only, so NoInit allows it (the directive must be the line comment).
//
//go:embed all:web/dist
var webDist embed.FS //archtest:allow-var go:embed needs a package-level var; embed.FS is immutable (06 §6.12.5)

// UIBundle returns hello's built SPA (07 §7.6).
func UIBundle() ui.Bundle { return ui.MustFromFS(webDist, "web/dist") }
