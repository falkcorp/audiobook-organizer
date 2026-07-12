// file: internal/plugins/plugins.go
// version: 1.4.0
// guid: f3a4b5c6-d7e8-9012-cdef-234567890123
// last-edited: 2026-07-11

// Package plugins is the central registration point for all UOS plugins.
// Import this package (blank import) in the server binary to register all
// plugin operations with the global UOS registry.
//
// As plugins are migrated (UOS-07 through UOS-12), their blank imports are
// added here. The server calls each plugin's Register method explicitly.
package plugins

import (
	// Dedup plugin — embed-scan, full-scan, llm-review, book-signature-scan ops (UOS-07 + UOS-09).
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/dedup"
	// Deluge plugin — protected-paths-sync, centralize, path-update ops (UOS-11).
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/deluge"
	// Metafetch plugin — read-only scoring calibration harness (INIT-3-T1).
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/metafetch"
)
