// file: internal/server/deluge_import.go
// version: 2.1.1
// guid: f3e7a9c1-2b4d-5086-d9f2-4e1c7b0a3e58
// last-edited: 2026-10-11
//
// Placeholder: the server package's importToLibrary shim was removed (U1000, no
// callers). The importer is deluge.ImportToLibrary (internal/deluge/import.go),
// which handleDiscoveryImport (deluge_discovery.go) calls directly. The deluge
// plugin's own importToLibrary is gone too (10-PR2).

package server
