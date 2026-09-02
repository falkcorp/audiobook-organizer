// file: internal/config/update_service.go
// version: 3.16.0
// guid: f6g7h8i9-j0k1-l2m3-n4o5-p6q7r8s9t0u1
// last-edited: 2026-09-02

package config

import (
	"context"
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
// config update that changed it has been persisted. The dedup plugin installs
// it (internal/plugins/dedup/register.go PostInit): the sink swaps the ladder
// into the live engine and QUEUES the re-band of stored candidates as the
// dedup.rescore operation, returning that operation's id so the PUT response
// can point at it. The ladder has already passed the same Validate the engine
// applies, so the engine cannot refuse it; the only thing that can fail here
// is the hand-off (engine unavailable, operation could not be queued).
//
// A sink error does NOT roll the update back. The ladder is valid, it is
// persisted, and it is (usually) already live in the engine — rolling memory
// and the blob back to the old ladder would leave the engine on the new one,
// a three-way disagreement worse than the failure being reported. Instead
// UpdateConfig returns 500 with the config left as saved and a message that
// says exactly that, plus the manual remedy:
// POST /api/v1/dedup/rescore {"apply":true}.
//
// ctx is the caller's request context so a queued hand-off is attributed to,
// and bounded by, the request that asked for it.
type DedupScoreConfigSink func(ctx context.Context, cfg unified.ScoreConfig) (rescoreOpID string, err error)

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
// Rejecting here, before the live config is touched, is the only place that
// closes the loop.
//
// The payload is unmarshalled into a DEEP COPY of the live config (Config.Clone)
// and only assigned over the live struct once it validates. Unmarshalling into
// the live struct — even under the write lock, even with `prior = *c` saved —
// leaked rejected map entries into the live Dedup.Signals.Confidence map,
// because Go map assignment is by reference and json.Unmarshal merges into a
// non-nil map in place; see Config.Clone for the incident.
//
// ctx is the HTTP request context; it bounds the dedup re-band hand-off.
func (us *UpdateService) UpdateConfig(ctx context.Context, payload map[string]any) (int, map[string]any) {
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
		// prior is a DEEP copy of the whole in-memory config as it stood
		// immediately before the update, captured under the write lock. It is
		// the rollback target when the save fails, and includes the secret
		// updates applied above so a rollback does not un-apply those.
		// Deep, not `prior = *c`: a struct assignment shares every map and
		// slice with the live config, so restoring it restored nothing.
		prior *Config
		// priorLadder / newLadder decide whether the dedup sink must run: a
		// PUT that touches only unrelated fields must not trigger a rescore.
		priorLadder, newLadder       unified.ScoreConfig
		priorLadderErr, newLadderErr error
	)
	Mutate(func(c *Config) {
		prior = c.Clone()
		priorLadder, priorLadderErr = prior.Dedup.Signals.ScoreConfig()

		// candidate is the SECOND deep copy: the payload is applied to it and
		// it is validated while the live config is untouched. Only a candidate
		// that passes is assigned over *c, so a rejected PUT leaves no trace —
		// not in a scalar field and not as a stray key in a shared map.
		candidate := c.Clone()

		// Captured INSIDE the lock, immediately before the unmarshal that would
		// clobber them, so no concurrent writer can slip between snapshot and
		// restore. See restoreMaskedCredentials for why this is needed.
		priorCreds := snapshotSourceCredentials(candidate.MetadataSources)

		if err := json.Unmarshal(payloadJSON, candidate); err != nil {
			unmarshalErr = err
			return
		}
		restoreMaskedCredentials(candidate.MetadataSources, priorCreds)
		// Post-process: trim root_dir whitespace, derive setup_complete
		candidate.RootDir = strings.TrimSpace(candidate.RootDir)
		candidate.SetupComplete = candidate.RootDir != ""

		// Validate the dedup ladder on the candidate. On failure nothing has
		// been written to *c, so there is nothing to restore.
		newLadder, newLadderErr = candidate.Dedup.Signals.ScoreConfig()
		if newLadderErr != nil {
			ladderErr = newLadderErr
			return
		}
		*c = *candidate
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
		// The in-memory config was already replaced; leaving it there would
		// make the process behave as if the update took while the blob (and
		// the next restart) disagree. Roll memory back to the deep copy so
		// memory and disk tell the same story — map contents included.
		Mutate(func(c *Config) { *c = *prior })
		slog.Error("failed to persist config; in-memory config rolled back", "err", err)
		return http.StatusInternalServerError, map[string]any{
			"error":   "failed to save configuration",
			"details": err.Error(),
		}
	}

	slog.Info("Configuration saved successfully")

	// Hand a changed dedup ladder to the live engine and queue the re-band of
	// stored candidates. A prior ladder that failed to convert (a blob written
	// before the guard above existed) counts as changed: the engine could not
	// have been running on it.
	//
	// No rollback on failure — see DedupScoreConfigSink. The saved ladder is
	// valid and stays; the response says the re-band did not start and how to
	// run it by hand.
	rescoreOpID := ""
	if sink := us.getDedupScoreSink(); sink != nil {
		ladderChanged := priorLadderErr != nil || !reflect.DeepEqual(priorLadder, newLadder)
		if ladderChanged {
			opID, err := sink(ctx, newLadder)
			if err != nil {
				slog.Error("config saved but dedup score ladder hand-off failed; stored candidates were NOT re-banded",
					"err", err, "remedy", DedupRescoreRemedy)
				return http.StatusInternalServerError, map[string]any{
					"error": "configuration was saved and is in effect, but the dedup engine hand-off failed: " + err.Error() +
						". Stored duplicate candidates may still carry the previous ladder's band until you " + DedupRescoreRemedy + ".",
					"details": err.Error(),
					"saved":   true,
					"config":  us.MaskSecrets(Snapshot()),
				}
			}
			rescoreOpID = opID
			slog.Info("dedup score ladder updated in live engine; stored-candidate re-band queued",
				"certain", newLadder.BandCertainMin, "high", newLadder.BandHighMin,
				"medium", newLadder.BandMediumMin, "review", newLadder.BandReviewMin,
				"rescore_op_id", opID)
		}
	}

	resp := map[string]any{
		"message": "configuration updated and saved to database",
		"config":  us.MaskSecrets(Snapshot()),
	}
	if rescoreOpID != "" {
		resp["message"] = "configuration updated and saved to database; stored duplicate candidates are being re-banded under the new score ladder (operation " + rescoreOpID + ")"
		resp["dedup_rescore_op_id"] = rescoreOpID
	}
	return http.StatusOK, resp
}

// DedupRescoreRemedy is the one operator instruction every "stored candidates
// were not re-banded" message ends with. It names the endpoint that exists —
// there is no `dedup.rescore` maintenance op to "run"; the re-band is
// POST /api/v1/dedup/rescore (internal/server/wire_dedup_routes.go), or the
// dedup.rescore operation the config PUT queues for you.
const DedupRescoreRemedy = `run POST /api/v1/dedup/rescore {"apply":true}`

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
func (us *UpdateService) ApplyUpdates(ctx context.Context, payload map[string]any) error {
	status, resp := us.UpdateConfig(ctx, payload)
	if status >= 400 {
		if errMsg, ok := resp["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		return fmt.Errorf("config update failed with status %d", status)
	}
	return nil
}
