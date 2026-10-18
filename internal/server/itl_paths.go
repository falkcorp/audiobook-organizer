// file: internal/server/itl_paths.go
// version: 1.2.0
// guid: 0b8e6f52-4c1d-4e7a-9a63-5d2f8c1e7b40
// last-edited: 2026-10-18
//
// Read-side helpers for the ITL endpoints that remain after iTunes write-back
// was removed (owner decision 2026-10-07: iTunes is import-only). The ITL is
// parsed, never written.

package server

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
)

// configuredITLPath returns the configured ITL path cleaned and validated, or
// "" when none is configured. An unset path is not an error: the PID census
// runs without the ITL and then reports tracks_in_itl as 0. A path that is set
// but invalid is an error. It never writes a response.
func configuredITLPath() (string, error) {
	rawPath := config.AppConfig.ITunes.LibraryITLPath
	if rawPath == "" {
		return "", nil
	}
	itlPath, err := pathvalidation.CleanAbsolutePath(rawPath)
	if err != nil {
		return "", fmt.Errorf("invalid iTunes library ITL path in config: %w", err)
	}
	return itlPath, nil
}
