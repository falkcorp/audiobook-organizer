// file: internal/books/config_wire.go
// version: 1.0.0
// guid: 7a3d5f81-0c64-4e92-b8a7-1d9e2f6c4b05
// last-edited: 2026-11-19

package books

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/books/appconfig"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/module"
)

// appConfigSpec is books' Manifest.AppConfig (F3-07): the declaration that gets
// the module Deps.Config, with the secrets its own rename table maps and no
// others (appconfig.SecretAdmit). It is declared whether or not the core config
// boot runs: without the boot Deps.Config is nil and nothing is reached.
func appConfigSpec() *module.AppConfigSpec {
	return &module.AppConfigSpec{Secrets: appconfig.SecretAdmit()}
}

// appConfigBase is the Config the projection starts from: the fields no
// rename-table row stores (class x: dropped by the table because Pebble is the
// only database and AI routing moved into ai_endpoints; and the database path,
// whose row is never-stored because core.storage.path is the kernel's own),
// at their factory values. HEAD's readers of them remain (organizer's
// DatabaseType switch, the AI backend constructors), so they keep a value
// instead of the zero one. The differential oracle names each of these as lossy
// (config_cutover_oracle_test.go).
func appConfigBase(dbPath string) config.Config {
	var c config.Config
	c.DatabaseType = "pebble"
	c.DatabasePath = dbPath
	c.AutoUpdate.WindowStart = 2
	c.AutoUpdate.WindowEnd = 5
	c.AIBackend.LocalEmbeddingModel = "bge-m3"
	c.AIBackend.LocalLLMModel = "qwen2.5:7b-instruct"
	// 05-ai.md:803: after the flip there is one path, the router over the
	// ai_endpoints rows. HEAD's default was off, so an install that never turned
	// routing on moves from the legacy single-backend path to the routed one.
	// This is a behaviour change, named in the release-note table.
	c.AIEndpointsRouting = true
	// metadata_sources is a residual (class i) row: unset in the blob, it keeps
	// this base value, HEAD's fallback list; a stored list replaces it.
	c.MetadataSources = config.DefaultMetadataSources()
	return c
}

// installAppConfig builds the Facade over Deps.Config and binds it behind
// config.AppConfig and its siblings. It does nothing, and returns false, while
// Deps.Config is nil (no core config boot: HEAD's loaders stay the source). The
// engine's first projection is published before it returns, so the boot steps
// and NewServer after it read the canonical settings.
func (m *booksModule) installAppConfig(ctx context.Context, r *module.Registrar, store *database.PebbleStore) (bool, error) {
	acc := r.Deps().Config
	if acc == nil {
		return false, nil
	}
	eng := appconfig.New(acc, config.Project,
		appconfig.WithBase(func() config.Config { return appConfigBase(m.dbPath) }),
		appconfig.WithResidual(&configResidual{store: store, acc: acc}))
	if err := eng.Start(ctx); err != nil {
		return false, fmt.Errorf("books: building the config projection: %w", err)
	}
	config.Install(eng)
	m.appConfigInstalled.Store(true)
	return true, nil
}

// releaseAppConfig unbinds the Facade installAppConfig bound, once.
func (m *booksModule) releaseAppConfig() {
	if m.appConfigInstalled.Swap(false) {
		config.Install(nil)
	}
}
