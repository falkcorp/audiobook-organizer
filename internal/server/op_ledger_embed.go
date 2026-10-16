// file: internal/server/op_ledger_embed.go
// version: 1.0.0
// guid: 5d2821c1-7deb-4209-a11f-889b016cb9e0
// last-edited: 2026-10-16

package server

import (
	_ "embed"
)

// embeddedOpLedger is the op-ID ledger (testdata/op_ids.golden): every op ID
// this server has ever registered, append-only. The file stays under
// testdata/ because TestOpIDs_NoRenameWithoutAlias and every census PR that
// appends to it use that path; embedding it lets the production startup
// check (opCatalogGate) read the same bytes. TestOpLedgerEmbedMatchesFile
// keeps the two equal.
//
// The Docker build context excludes only the top-level testdata/ directory
// (.dockerignore), so this file is in it.
//
//go:embed testdata/op_ids.golden
var embeddedOpLedger []byte
