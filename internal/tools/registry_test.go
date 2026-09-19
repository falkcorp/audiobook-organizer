// file: internal/tools/registry_test.go
// version: 1.1.0
// guid: a7b8c9d0-e1f2-3456-abcd-456789012345
// last-edited: 2026-09-19

package tools_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_SystemMode_Found(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fpcalc")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	cfg := &tools.ToolsConfig{Fpcalc: tools.ToolConfig{Mode: tools.ToolModeSystem}}
	r := tools.NewToolRegistry(cfg)
	rel, ok := tools.LatestRelease("fpcalc")
	require.True(t, ok)
	r.Register(tools.ToolDef{Name: "fpcalc", Release: rel})

	path, err := r.Resolve("fpcalc")
	require.NoError(t, err)
	assert.Equal(t, bin, path)
	assert.True(t, r.Available("fpcalc"))
}

func TestRegistry_DisabledMode(t *testing.T) {
	cfg := &tools.ToolsConfig{Fpcalc: tools.ToolConfig{Mode: tools.ToolModeDisabled}}
	r := tools.NewToolRegistry(cfg)
	rel, ok := tools.LatestRelease("fpcalc")
	require.True(t, ok)
	r.Register(tools.ToolDef{Name: "fpcalc", Release: rel})

	_, err := r.Resolve("fpcalc")
	assert.ErrorIs(t, err, tools.ErrToolNotAvailable)
	assert.False(t, r.Available("fpcalc"))
}

func TestRegistry_CustomMode(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "myfpcalc")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))

	cfg := &tools.ToolsConfig{Fpcalc: tools.ToolConfig{Mode: tools.ToolModeCustom, CustomPath: bin}}
	r := tools.NewToolRegistry(cfg)
	rel, ok := tools.LatestRelease("fpcalc")
	require.True(t, ok)
	r.Register(tools.ToolDef{Name: "fpcalc", Release: rel})

	path, err := r.Resolve("fpcalc")
	require.NoError(t, err)
	assert.Equal(t, bin, path)
}

func TestRegistry_UnknownTool(t *testing.T) {
	cfg := &tools.ToolsConfig{}
	r := tools.NewToolRegistry(cfg)
	_, err := r.Resolve("nonexistent")
	assert.ErrorIs(t, err, tools.ErrToolNotAvailable)
}

// A config blob persisted before the tools block existed loads Mode "" over
// InitConfig's default. That must resolve as system mode, not "unknown mode"
// (prod ran with fpcalc unresolvable through the registry for over a week).
func TestRegistry_EmptyModeResolvesAsSystem(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fpcalc")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	r := tools.NewToolRegistry(&tools.ToolsConfig{})
	rel, ok := tools.LatestRelease("fpcalc")
	require.True(t, ok)
	r.Register(tools.ToolDef{Name: "fpcalc", Release: rel})

	path, err := r.Resolve("fpcalc")
	require.NoError(t, err)
	assert.Equal(t, bin, path)
	assert.Equal(t, tools.ToolModeSystem, r.Status("fpcalc").Mode)
}
