// file: core/integrations/download/paths.go
// version: 2.0.0
// guid: 0d2b7e94-3c58-4a16-9e0f-b41c7a8d5f23
// last-edited: 2026-11-22

package download

import "github.com/falkcorp/mediacore/integrations"

// Path mapping (05-download §4.4): what a download client reports is a path on
// ITS host, and the library sees the same bytes under one of its roots. The
// mappings are library.Mapping rows (core.library.mappings) and the Pool
// applies them through library.PathMapper, so there is one mapper, one set of
// matching rules (longest prefix on a separator boundary, Fold for Windows
// remotes, no guessed path) and one library.ErrUnmapped. This package once kept
// a download-local copy of all of it (F7-04, seams.d/F7-04.md); F7-09 D4
// deleted it.

// Source is the mapping source of a download instance: "download:<id>", the
// library.Mapping.Source of every row that maps its client's paths.
func Source(id integrations.InstanceID) string { return "download:" + string(id) }
