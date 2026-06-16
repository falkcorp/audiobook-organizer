// file: internal/fingerprint/fpcalc_registry_test.go
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678902
// last-edited: 2026-06-15

package fingerprint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetResolvedFpcalcPath(t *testing.T) {
	// Clean state before and after.
	fingerprint.SetResolvedFpcalcPath("")
	t.Cleanup(func() { fingerprint.SetResolvedFpcalcPath("") })

	dir := t.TempDir()
	bin := filepath.Join(dir, "fpcalc")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))

	fingerprint.SetResolvedFpcalcPath(bin)
	assert.True(t, fingerprint.Available())
}
