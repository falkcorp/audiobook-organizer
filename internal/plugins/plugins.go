// file: internal/plugins/plugins.go
// version: 1.5.0
// guid: f3a4b5c6-d7e8-9012-cdef-234567890123
// last-edited: 2026-10-01

// Package plugins is the one list of UOS plugins that register through the
// service registry. internal/server blank-imports this package, so a plugin
// listed here is linked into the server binary and its PostInit registers its
// ops; a plugin missing from here is not, and its ops do not exist in prod.
//
// metafetch was listed here while nothing in the binary imported this
// package, so from INIT-3 until 2026-10-01 its ops (calibrate-scoring,
// asin-backfill) were never registered: tests passed only because a test file
// imported the plugin. TestEveryRegistryPluginIsLinkedIntoTheBinary reads the
// binary's real import graph to keep that from happening again.
package plugins

import (
	// AcoustID plugin — fingerprint backfill and index ops.
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/acoustid"
	// Dedup plugin — embed-scan, full-scan, llm-review, book-signature-scan ops (UOS-07 + UOS-09).
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/dedup"
	// Deluge plugin — protected-paths-sync, centralize, path-update ops (UOS-11).
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/deluge"
	// iTunes plugin — sync, path reconcile/repair ops.
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/itunes"
	// Metafetch plugin — scoring calibration (INIT-3-T1) and the Audible
	// ASIN/ISBN backfill.
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/metafetch"
)
