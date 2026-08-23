// file: internal/server/batch_poller_register.go
// version: 1.2.0
// last-edited: 2026-08-23

package server

import (
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:  "batchpoller",
		Needs: []string{serviceregistry.KeyStore, serviceregistry.KeyConfig},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.OperationStore](c, serviceregistry.KeyStore)
			cfg := config.GetConfig(c)

			// Pre-condition: OpenAI API key and AI parsing must be enabled
			if cfg.OpenAIAPIKey == "" || !cfg.EnableAIParsing {
				slog.Info("batchpoller skipping (OpenAI disabled or API key not set)")
				return nil, nil
			}

			// Create the OpenAI parser instance and BatchPoller
			parser := ai.NewOpenAIParser(cfg, cfg.OpenAIAPIKey, cfg.EnableAIParsing)
			poller := NewBatchPoller(store, parser)
			slog.Info("batchpoller initialized")
			return poller, nil
		},
	})
}
