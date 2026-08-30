// file: internal/config/vector_backend_default_test.go
// version: 1.1.0
// guid: 3f6a1c02-9b4e-4d71-8c2a-5e0d7b91a4c6
// last-edited: 2026-08-30

package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResetToDefaults_VectorBackendIsHNSW pins the SECOND default site.
//
// There are two independent places a fresh vector backend gets chosen:
// viper.SetDefault("embedding.vector_backend", ...) in InitConfig, and the
// hardcoded struct literal in ResetToDefaults. TestInitConfig_EmbeddingDefaults
// covers the first; it goes through viper and would stay green if only the
// literal were wrong. This test never touches viper — it calls ResetToDefaults
// and reads the resulting AppConfig — so the two are genuinely separate
// instruments and each mutation reddens exactly one of them.
func TestResetToDefaults_VectorBackendIsHNSW(t *testing.T) {
	// ResetToDefaults overwrites the package-global AppConfig wholesale. Several
	// existing tests in this package call it without restoring, so nothing here
	// depends on the current value — but leaving factory defaults behind couples
	// this test to whatever runs after it, so restore explicitly.
	before := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = before }) })

	ResetToDefaults()
	snap := Snapshot()

	assert.Equal(t, "hnsw", snap.Embedding.VectorBackend,
		"ResetToDefaults must not hand out the brute-force chromem scan")
}

// TestValidate_VectorBackendEmptyNormalizesToHNSW covers the upgrade path,
// which neither default site reaches.
//
// migrateEmbeddingBlob (persistence.go) rewrites a legacy flat config_blob into
// the nested shape and carries old.VectorIndexBackend across verbatim. On a
// blob that predates the field that value is "", and the blob is applied over
// AppConfig wholesale, so viper.SetDefault never gets a say. Before this fix,
// registry_wire.go's `== "hnsw"` test then fell through to chromem for every
// such install — i.e. flipping only the two defaults would have left the trap
// live for the entire upgraded population.
func TestValidate_VectorBackendEmptyNormalizesToHNSW(t *testing.T) {
	c := &Config{DatabaseType: "pebble"}
	c.Embedding.VectorBackend = ""

	_ = c.Validate() // other fields may fail; we only care about this one

	assert.Equal(t, "hnsw", c.Embedding.VectorBackend,
		"an empty vector_backend must normalize to the default, not fall through to chromem")
}

// TestValidate_VectorBackendEnum locks the enum check. Before this change an
// unknown value was accepted silently and registry_wire.go's exact-match test
// quietly selected chromem, so a typo cost two orders of magnitude with no
// error surface at all.
//
// This check has real reach: besides cmd/root.go, the config PUT handler
// (internal/server/handlers/system/handler.go) runs Validate() after applying a
// payload and rolls the whole config back with a 400 when it fails. So a typo'd
// vector_backend can no longer be persisted through the API either.
//
// Note that the handler validates a Snapshot() — a copy — so the empty-string
// normalization below heals the copy, not the stored global. That is exactly
// why resolveVectorBackend in internal/server also maps "" to hnsw: the
// selection site is the only place guaranteed to see the value that is really
// stored.
func TestValidate_VectorBackendEnum(t *testing.T) {
	t.Run("chromem is still accepted", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble"}
		c.Embedding.VectorBackend = "chromem"
		err := c.Validate()
		if err != nil {
			assert.NotContains(t, err.Error(), "vector_backend",
				"chromem must remain a selectable fallback")
		}
		assert.Equal(t, "chromem", c.Embedding.VectorBackend,
			"an explicit chromem choice must be preserved, not normalized away")
	})

	t.Run("hnsw is accepted", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble"}
		c.Embedding.VectorBackend = "hnsw"
		if err := c.Validate(); err != nil {
			assert.NotContains(t, err.Error(), "vector_backend")
		}
	})

	t.Run("unknown is rejected", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble"}
		c.Embedding.VectorBackend = "hnws" // transposed typo
		err := c.Validate()
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "embedding.vector_backend must be 'hnsw' or 'chromem'"),
			"unknown backend must be rejected, not silently downgraded to a brute-force scan; got: %v", err)
	})
}

// TestInitConfig_VectorBackendDefaultViaViper is the viper-path twin of the
// ResetToDefaults test above, kept here next to it so the pair is obvious.
// TestInitConfig_EmbeddingDefaults asserts the same thing among the other
// embedding fields; this one isolates the backend so a mutation of
// viper.SetDefault reddens a test that names the failure.
func TestInitConfig_VectorBackendDefaultViaViper(t *testing.T) {
	viper.Reset()
	InitConfig()

	assert.Equal(t, "hnsw", viper.GetString("embedding.vector_backend"),
		"the viper default must be hnsw, not the brute-force chromem scan")
}
