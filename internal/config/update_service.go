// file: internal/config/update_service.go
// version: 3.15.0
// guid: f6g7h8i9-j0k1-l2m3-n4o5-p6q7r8s9t0u1
// last-edited: 2026-09-02

package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// DedupScoreConfigSink receives the effective dedup score ladder after a
// config update that changed it has been persisted. registry_wire.go installs
// the live engine's reload here so a PUT /api/v1/config that edits
// dedup.signals reaches scoring immediately instead of at the next restart.
// Returning an error rolls the update back (memory AND the persisted blob) and
// fails the request: a ladder the engine refused must not be left persisted,
// where it would refuse the NEXT startup too.
type DedupScoreConfigSink func(unified.ScoreConfig) error

// UpdateService handles applying and persisting config changes.
type UpdateService struct {
	DB database.SettingsStore

	sinkMu         sync.RWMutex
	dedupScoreSink DedupScoreConfigSink
}

// NewUpdateService creates a new UpdateService.
func NewUpdateService(db database.SettingsStore) *UpdateService {
	return &UpdateService{DB: db}
}

// SetDedupScoreConfigSink installs (or, with nil, removes) the callback that
// receives the dedup score ladder after an update changes it. Set once at
// wiring time; guarded anyway so a concurrent UpdateConfig never observes a
// torn function value.
func (us *UpdateService) SetDedupScoreConfigSink(sink DedupScoreConfigSink) {
	us.sinkMu.Lock()
	defer us.sinkMu.Unlock()
	us.dedupScoreSink = sink
}

func (us *UpdateService) getDedupScoreSink() DedupScoreConfigSink {
	us.sinkMu.RLock()
	defer us.sinkMu.RUnlock()
	return us.dedupScoreSink
}

// ValidateUpdate checks that the payload is non-empty.
func (us *UpdateService) ValidateUpdate(payload map[string]any) error {
	if len(payload) == 0 {
		return fmt.Errorf("no configuration updates provided")
	}
	return nil
}

// MaskSecrets returns a copy of cfg with all secret fields masked for API responses.
func (us *UpdateService) MaskSecrets(cfg Config) Config {
	masked := cfg
	if masked.OpenAIAPIKey != "" {
		masked.OpenAIAPIKey = database.MaskSecret(masked.OpenAIAPIKey)
	}
	if masked.AcoustIDAPIKey != "" {
		masked.AcoustIDAPIKey = database.MaskSecret(masked.AcoustIDAPIKey)
	}
	if masked.GoogleBooksAPIKey != "" {
		masked.GoogleBooksAPIKey = database.MaskSecret(masked.GoogleBooksAPIKey)
	}
	if masked.HardcoverAPIToken != "" {
		masked.HardcoverAPIToken = database.MaskSecret(masked.HardcoverAPIToken)
	}
	if masked.BasicAuthPassword != "" {
		masked.BasicAuthPassword = database.MaskSecret(masked.BasicAuthPassword)
	}
	masked.MetadataSources = maskMetadataSourceCredentials(cfg.MetadataSources)
	return masked
}

// maskMetadataSourceCredentials returns a deep copy of srcs with every
// credential value masked.
//
// This closed a real leak: GET /api/v1/config masked the scalar
// GoogleBooksAPIKey ("AIz****35bE") while returning the SAME key in full
// cleartext at metadata_sources[].credentials.apiKey — two different maskings
// of one secret in one response. Any provider credential stored here (Hardcover
// token, a future provider's key) leaked the same way.
//
// The deep copy is load-bearing, not defensive style. MaskSecrets does
// `masked := cfg`, which is a SHALLOW struct copy: the MetadataSources slice
// header is copied but the backing array is shared with the live AppConfig, and
// each Credentials map is a reference. Masking in place would therefore
// overwrite the process's real credentials with "AIz****35bE" and every
// provider client would start authenticating with the mask. So the slice is
// reallocated and each map is rebuilt.
//
// Empty values are left as "" rather than masked, so the UI can still tell
// "not configured" apart from "configured but hidden".
func maskMetadataSourceCredentials(srcs []MetadataSource) []MetadataSource {
	if srcs == nil {
		return nil
	}
	out := make([]MetadataSource, len(srcs))
	copy(out, srcs)
	for i := range out {
		if out[i].Credentials == nil {
			continue
		}
		creds := make(map[string]string, len(out[i].Credentials))
		for k, v := range out[i].Credentials {
			if v == "" {
				creds[k] = ""
				continue
			}
			creds[k] = database.MaskSecret(v)
		}
		out[i].Credentials = creds
	}
	return out
}

// snapshotSourceCredentials copies every metadata-source credential, keyed by
// source ID, so restoreMaskedCredentials can put back anything the incoming
// payload would have destroyed.
func snapshotSourceCredentials(srcs []MetadataSource) map[string]map[string]string {
	out := make(map[string]map[string]string, len(srcs))
	for _, s := range srcs {
		if len(s.Credentials) == 0 {
			continue
		}
		creds := make(map[string]string, len(s.Credentials))
		maps.Copy(creds, s.Credentials)
		out[s.ID] = creds
	}
	return out
}

// restoreMaskedCredentials puts back any metadata-source credential that the
// incoming payload did not genuinely supply.
//
// This exists because MaskSecrets now masks these values, which turns a
// previously harmless round-trip into a destructive one. The scalar secrets
// (openai_api_key and friends) are safe: UpdateConfig deletes them from the
// payload via secretFieldKeys before the JSON round-trip, so echoing a masked
// scalar back is discarded. metadata_sources is NOT in that list, and
// json.Unmarshal REPLACES the whole slice, credentials included. So a client
// that does the obvious GET /config -> edit -> PUT /config would write
// "AIz****35bE" over the real key and SaveConfigToDatabase would persist it --
// masking the response would have converted a disclosure bug into permanent
// credential loss.
//
// Two cases are restored:
//
//   - the incoming value is exactly MaskSecret(previous): the client echoed
//     back what GET handed it, which never means "change the key to this".
//   - the incoming value is empty while a previous value existed: either the
//     key was absent (PUT /config is a merge -- absent means unchanged) or it
//     was explicitly cleared. Those are indistinguishable after unmarshal, so
//     this deliberately refuses to destroy a credential through an ambiguous
//     path. Clearing one means removing the source or supplying a new value.
//
// A genuinely new value is always written through.
func restoreMaskedCredentials(srcs []MetadataSource, prior map[string]map[string]string) {
	for i := range srcs {
		old, ok := prior[srcs[i].ID]
		if !ok {
			continue
		}
		if srcs[i].Credentials == nil {
			// The payload dropped the map entirely; rebuild it from the prior
			// values rather than silently losing every credential.
			restored := make(map[string]string, len(old))
			maps.Copy(restored, old)
			srcs[i].Credentials = restored
			continue
		}
		for k, prev := range old {
			if prev == "" {
				continue
			}
			switch srcs[i].Credentials[k] {
			case prev:
				// unchanged
			case "", database.MaskSecret(prev):
				srcs[i].Credentials[k] = prev
			}
		}
	}
}

// acceptSecretUpdate reports whether an incoming scalar secret should be written
// through to the stored config.
//
// GET /api/v1/config returns these masked, so a client that reads the config and
// PUTs it back sends "AIz****35bE" as the value. Writing that through replaces
// the real key with the mask. The browser avoids this deliberately -- Settings.tsx
// loads the config with `openaiApiKey: ”` ("Clear field when loading, show
// placeholder instead") and useSettingsHandlers.ts omits the key entirely when
// empty -- but that is a client-side convention, not an invariant. Any other
// client doing the obvious GET-edit-PUT destroys the key.
//
// The failure is silent in a nasty way: MaskSecret is idempotent
// (MaskSecret("AIz****35bE") == "AIz****35bE"), so the response after a
// destructive write is byte-identical to the response after a successful one.
// The UI still displays "AIz****35bE" and nothing surfaces until the provider
// starts returning 401.
//
// Only the exact mask is rejected. An empty value still clears the secret --
// that is existing, intentional behaviour and the only way to unset one. This is
// deliberately narrower than restoreMaskedCredentials, which also restores on
// empty: a metadata-source payload cannot express "clear this credential"
// (the client always sends a credentials map), so there an empty value is
// ambiguous. Here it is unambiguous.
func acceptSecretUpdate(incoming, current string) bool {
	if current == "" || incoming == "" {
		return true
	}
	return incoming != database.MaskSecret(current)
}

// secretFieldKeys are extracted and applied explicitly, then removed before the
// JSON round-trip so they are never stored in plaintext in the config blob.
var secretFieldKeys = []string{
	"openai_api_key",
	"acoustid_api_key",
	"google_books_api_key",
	"hardcover_api_token",
	"basic_auth_password",
}

// immutableFieldKeys cannot be changed at runtime and are rejected if present.
var immutableFieldKeys = []string{"database_type", "enable_sqlite"}

// UpdateConfig applies a config update payload to AppConfig and persists it.
//
// Architecture: non-secret fields are applied via JSON round-trip onto AppConfig.
// json.Unmarshal only overwrites keys present in the JSON, so absent keys leave
// AppConfig unchanged. This means any new field added to Config is
// automatically handled here with no registration required.
//
// dedup.signals is validated BEFORE the blob is persisted. Every other field
// is validated by Config.Validate in the HTTP handler AFTER SaveConfigToDatabase
// has already written the blob (see handlers/system/handler.go UpdateConfig —
// a pre-existing gap tracked in todo.d/). The dedup ladder cannot live with
// that ordering because registry_wire.go refuses to build the engine on an
// invalid ladder: one persisted bad value is a crash loop on every restart,
// and the blob overrides config.yaml so editing the file cannot repair it.
// Rejecting here, with the in-memory config restored under the same write
// lock, is the only place that closes the loop.
func (us *UpdateService) UpdateConfig(payload map[string]any) (int, map[string]any) {
	if us.DB == nil {
		return http.StatusInternalServerError, map[string]any{"error": "database not initialized"}
	}
	if payload == nil {
		return http.StatusBadRequest, map[string]any{"error": "configuration payload is required"}
	}

	// Reject immutable fields
	for _, field := range immutableFieldKeys {
		if _, ok := payload[field]; ok {
			return http.StatusBadRequest, map[string]any{"error": field + " cannot be changed at runtime"}
		}
	}

	// Apply secrets explicitly — they need masking/debug logging and must not
	// flow through the JSON round-trip to avoid plaintext exposure.
	// WHY Mutate: each assignment here is a write to the global AppConfig that
	// races with concurrent HTTP readers; Mutate serialises under the write lock.
	// The acceptSecretUpdate check lives INSIDE Mutate on purpose: it compares
	// the incoming value against the currently stored one, so reading the
	// current value outside the lock and writing inside it would be a
	// check-then-act race with any concurrent writer.
	if val, ok := payloadString(payload, "openai_api_key"); ok {
		slog.Debug("UpdateConfig updating OpenAI API key (len)", "val_count", len(val))
		Mutate(func(c *Config) {
			if acceptSecretUpdate(val, c.OpenAIAPIKey) {
				c.OpenAIAPIKey = val
			}
		})
	}
	if val, ok := payloadString(payload, "acoustid_api_key"); ok {
		slog.Debug("UpdateConfig updating AcoustID API key (len)", "val_count", len(val))
		Mutate(func(c *Config) {
			if acceptSecretUpdate(val, c.AcoustIDAPIKey) {
				c.AcoustIDAPIKey = val
			}
		})
	}
	if val, ok := payloadString(payload, "google_books_api_key"); ok {
		Mutate(func(c *Config) {
			if acceptSecretUpdate(val, c.GoogleBooksAPIKey) {
				c.GoogleBooksAPIKey = val
			}
		})
	}
	if val, ok := payloadString(payload, "hardcover_api_token"); ok {
		Mutate(func(c *Config) {
			if acceptSecretUpdate(val, c.HardcoverAPIToken) {
				c.HardcoverAPIToken = val
			}
		})
	}
	if val, ok := payloadString(payload, "basic_auth_password"); ok {
		Mutate(func(c *Config) {
			if acceptSecretUpdate(val, c.BasicAuthPassword) {
				c.BasicAuthPassword = val
			}
		})
	}

	// Build filtered payload without secrets (already applied above)
	filtered := make(map[string]any, len(payload))
	maps.Copy(filtered, payload)
	for _, k := range secretFieldKeys {
		delete(filtered, k)
	}

	// remapScheduledKeys handles the deeper two-level scheduled_* nesting that the
	// retired generic shim never covered (owned by INIT-6/WF-3); it stays.
	filtered = remapScheduledKeys(filtered)

	// Apply all remaining fields via JSON round-trip.
	// Any field in Config with a matching json tag is set automatically.
	// WHY Mutate: json.Unmarshal writes multiple fields in sequence; without the
	// write lock a concurrent Snapshot() call could observe a half-written struct.
	payloadJSON, err := json.Marshal(filtered)
	if err != nil {
		return http.StatusBadRequest, map[string]any{"error": "failed to encode payload: " + err.Error()}
	}
	var (
		unmarshalErr error
		ladderErr    error
		// prior is the whole in-memory config as it stood immediately before
		// the unmarshal, captured under the write lock. It is the rollback
		// target for every failure below (ladder rejected, save failed, engine
		// refused), and includes the secret updates applied above so a
		// rollback does not un-apply those in memory.
		prior Config
		// priorLadder / newLadder decide whether the dedup sink must run: a
		// PUT that touches only unrelated fields must not trigger a rescore.
		priorLadder, newLadder       unified.ScoreConfig
		priorLadderErr, newLadderErr error
	)
	Mutate(func(c *Config) {
		prior = *c
		priorLadder, priorLadderErr = prior.Dedup.Signals.ScoreConfig()

		// Captured INSIDE the lock, immediately before the unmarshal that would
		// clobber them, so no concurrent writer can slip between snapshot and
		// restore. See restoreMaskedCredentials for why this is needed.
		priorCreds := snapshotSourceCredentials(c.MetadataSources)

		if err := json.Unmarshal(payloadJSON, c); err != nil {
			unmarshalErr = err
			return
		}
		restoreMaskedCredentials(c.MetadataSources, priorCreds)
		// Post-process inside the lock: trim root_dir whitespace, derive setup_complete
		c.RootDir = strings.TrimSpace(c.RootDir)
		c.SetupComplete = c.RootDir != ""

		// Validate the dedup ladder while still holding the lock, and restore
		// the prior config in the same critical section on failure so no
		// reader ever observes the rejected values.
		newLadder, newLadderErr = c.Dedup.Signals.ScoreConfig()
		if newLadderErr != nil {
			ladderErr = newLadderErr
			*c = prior
		}
	})
	if unmarshalErr != nil {
		return http.StatusBadRequest, map[string]any{"error": "failed to apply config: " + unmarshalErr.Error()}
	}
	if ladderErr != nil {
		slog.Warn("config update rejected: dedup score ladder invalid; nothing persisted", "err", ladderErr)
		return http.StatusBadRequest, map[string]any{
			"error": "invalid configuration: " + ladderErr.Error() +
				" — nothing was saved; the dedup score ladder must satisfy 100 ≥ band_certain_min > band_high_min > band_medium_min > band_review_min ≥ 0 (0–100 scale)",
		}
	}

	if err := SaveConfigToDatabase(us.DB); err != nil {
		// The in-memory config was already mutated; leaving it there would
		// make the process behave as if the update took while the blob (and
		// the next restart) disagree. Roll memory back so memory and disk
		// tell the same story.
		Mutate(func(c *Config) { *c = prior })
		slog.Error("failed to persist config; in-memory config rolled back", "err", err)
		return http.StatusInternalServerError, map[string]any{
			"error":   "failed to save configuration",
			"details": err.Error(),
		}
	}

	slog.Info("Configuration saved successfully")

	// Push a changed dedup ladder to the live engine. A prior ladder that
	// failed to convert (a blob written before the guard above existed)
	// counts as changed: the engine could not have been running on it.
	if sink := us.getDedupScoreSink(); sink != nil {
		ladderChanged := priorLadderErr != nil || !reflect.DeepEqual(priorLadder, newLadder)
		if ladderChanged {
			if err := sink(newLadder); err != nil {
				// The engine refused a ladder that passed Validate — roll back
				// memory AND the blob so the same value cannot refuse the next
				// startup. If the re-save itself fails there is nothing more
				// this path can do but say so loudly.
				Mutate(func(c *Config) { *c = prior })
				if rerr := SaveConfigToDatabase(us.DB); rerr != nil {
					slog.Error("dedup engine refused new score ladder AND rollback re-save failed; persisted blob may hold a ladder the engine rejects",
						"sink_err", err, "resave_err", rerr)
					return http.StatusInternalServerError, map[string]any{
						"error":   "dedup engine rejected the new score ladder and the rollback could not be persisted",
						"details": err.Error() + "; rollback: " + rerr.Error(),
					}
				}
				slog.Error("dedup engine refused new score ladder; config update rolled back", "err", err)
				return http.StatusInternalServerError, map[string]any{
					"error":   "dedup engine rejected the new score ladder; configuration rolled back",
					"details": err.Error(),
				}
			}
			slog.Info("dedup score ladder updated in live engine",
				"certain", newLadder.BandCertainMin, "high", newLadder.BandHighMin,
				"medium", newLadder.BandMediumMin, "review", newLadder.BandReviewMin)
		}
	}

	return http.StatusOK, map[string]any{
		"message": "configuration updated and saved to database",
		"config":  us.MaskSecrets(Snapshot()),
	}
}

// payloadString extracts a string value from the payload if present and non-empty.
func payloadString(payload map[string]any, key string) (string, bool) {
	v, ok := payload[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// ApplyUpdates applies config updates and persists them.
// Deprecated: prefer UpdateConfig directly.
func (us *UpdateService) ApplyUpdates(payload map[string]any) error {
	status, resp := us.UpdateConfig(payload)
	if status >= 400 {
		if errMsg, ok := resp["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		return fmt.Errorf("config update failed with status %d", status)
	}
	return nil
}
