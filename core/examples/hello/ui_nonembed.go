//go:build !embed_frontend

// file: core/examples/hello/ui_nonembed.go
// version: 0.1.0
// guid: 6a2f8d15-0e4b-4c73-b9d8-3f1e7a5c2b90
// last-edited: 2026-11-20

package hello

import "github.com/falkcorp/mediacore/ui"

// UIBundle returns the zero Bundle: the API-only build (07 §7.6.3).
func UIBundle() ui.Bundle { return ui.Bundle{} }
