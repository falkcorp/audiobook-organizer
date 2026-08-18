// file: internal/itunes/service/store.go
// version: 1.1.0
// last-edited: 2026-08-18
// guid: 4f9bbf9f-0d28-46d5-be9c-e9ce3a422593

// Package itunesservice contains the iTunes integration: import pipeline,
// ITL write-back batcher, position sync, path reconcile, playlist sync,
// track provisioner, and ITL transfer. The low-level ITL parser, fingerprint,
// path mapping, and smart-criteria translator live in the parent package
// internal/itunes and are untouched by this extraction.
//
// See docs/superpowers/specs/2026-04-18-itunes-service-extraction-design.md.
package itunesservice

// Store is what internal/server hands to New, and its only job is to be
// assignable to each subsystem's own store as service.go wires them up. Every
// entry below is a surface that was measured against its consumer with an
// empty-interface compiler probe, so this composition carries no method that
// nothing in the package calls.
//
// It was 17 embeds of database.* -- roughly 171 methods, most of them unreached.
// That width was never a statement about iTunes being a hub; it was the sum of
// six subsystems and an import pipeline each holding the aggregate rather than
// the slice it used. The fix was applied to those parameter types first
// (writeback_batcher.go, path_reconcile.go, playlist_sync.go,
// track_provisioner.go, position_sync.go, path_repair.go, importer.go), and this
// declaration then followed from them rather than being negotiated on its own.
//
// Adding a method here should feel wrong: put it on the subsystem interface that
// needs it, in that subsystem's file, and let this stay a list of names.
type Store interface {
	WriteBackStore
	pathReconcilerStore
	pathRepairerStore
	playlistSyncStore
	positionSyncStore
	provisionerStore
	importerStore
}
