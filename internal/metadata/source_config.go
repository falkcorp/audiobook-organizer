// file: internal/metadata/source_config.go
// version: 1.0.0
// guid: de8cfa29-f920-44ce-8b68-6bece1e0f32b
// last-edited: 2026-08-20

package metadata

import "github.com/falkcorp/audiobook-organizer/internal/config"

// resolveBaseURL returns the configured BaseURL override for the given
// metadata provider ID (config.AppConfig.MetadataSources — settable via the
// AcousticDedup/Metadata Sources UI or the provider's *_BASE_URL env var,
// see viper.BindEnv in InitConfig), falling back to fallback when unset.
func resolveBaseURL(providerID, fallback string) string {
	for _, s := range config.AppConfig.MetadataSources {
		if s.ID == providerID && s.BaseURL != "" {
			return s.BaseURL
		}
	}
	return fallback
}
