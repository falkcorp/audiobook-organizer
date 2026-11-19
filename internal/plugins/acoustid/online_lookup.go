// file: internal/plugins/acoustid/online_lookup.go
// version: 1.8.0
// guid: 6e7f8091-a2b3-c4d5-e6f7-08192a3b4c5d
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/acoustid"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// onlineLookupKind is rule R3's native kind for acoustid.lookup-online.
const onlineLookupKind = tasks.Kind("books.acoustid.lookup-online")

// onlineLookupHelp is the definition's description, HEAD's and the native kind's
// Help (rule R5: verbatim).
const onlineLookupHelp = "For every BookFile with a stored whole-file chromaprint, queries the acoustid.org /v2/lookup API for a MusicBrainz recording match. Stores the top recording_id + score when score >= 0.85. Requires the ACOUSTID_API_KEY env var."

// LookupFunc is one AcoustID.org /v2/lookup call: the compressed fingerprint
// string and the whole-file duration in seconds.
type LookupFunc func(ctx context.Context, fingerprint string, durationSec int) (acoustid.LookupResult, error)

// LookupDeps is acoustid.lookup-online's access to AcoustID.org and the clock,
// read from process state by HEAD's body and handed in by its owner for the
// native kind (rule R10). A nil field is HEAD's own behaviour.
type LookupDeps struct {
	// APIKey returns the configured AcoustID API key ("" means not configured):
	// config.AppConfig.AcoustIDAPIKey.
	APIKey func() string
	// NewLookup builds the lookup call for an API key: acoustid.NewClient(key).Lookup.
	NewLookup func(apiKey string) LookupFunc
	// Wait blocks for d or until ctx is done (returning ctx.Err()): the pacing
	// between requests, a time.After select.
	Wait func(ctx context.Context, d time.Duration) error
}

// lookupHooks is LookupDeps with every nil field filled in with HEAD's own.
type lookupHooks struct {
	APIKey    func() string
	NewLookup func(apiKey string) LookupFunc
	Wait      func(ctx context.Context, d time.Duration) error
}

func (d LookupDeps) hooks() lookupHooks {
	h := lookupHooks(d)
	if h.APIKey == nil {
		h.APIKey = func() string { return config.AppConfig.AcoustIDAPIKey }
	}
	if h.NewLookup == nil {
		h.NewLookup = func(apiKey string) LookupFunc { return acoustid.NewClient(apiKey).Lookup }
	}
	if h.Wait == nil {
		h.Wait = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	return h
}

// onlineLookupEnv is what the online-lookup core takes from its shell (rules R2
// and R10): reads through Store, the one write through Writer.
type onlineLookupEnv struct {
	kindEnv
	Store  kindStore
	Writer kindWriter
	Lookup lookupHooks
}

// onlineLookupResult is the native run's result: the tallies in the closing
// message. HEAD stored no result, so its shell drops it.
type onlineLookupResult struct {
	Total              int `json:"total"`
	Matched            int `json:"matched"`
	NoMatch            int `json:"no_match"`
	Failed             int `json:"failed"`
	RateLimited        int `json:"rate_limited"`
	NeedsRefingerprint int `json:"needs_refingerprint"`
}

// AcoustIDOnlineMinScore is the minimum top-result score we treat as a
// real match. Below this the recording is more likely an unrelated
// upload that happened to collide. Tunable later.
const AcoustIDOnlineMinScore = 0.85

// onlineLookupThrottleMin paces requests to acoustid.org under normal
// operation. The free tier is 3 req/sec per API key; 400ms = 2.5 req/sec
// gives a safety margin even when responses are fast.
const onlineLookupThrottleMin = 400 * time.Millisecond

// onlineLookupThrottleMax is the throttle the op widens to after a 429
// surfaces from the client (rate-limited). ~0.5 req/sec is well below
// any reasonable ceiling and lets the limiter's window decay.
const onlineLookupThrottleMax = 2 * time.Second

// OnlineLookupParams controls the scope of an online lookup run.
type OnlineLookupParams struct {
	// Force re-queries even files that already have an
	// AcoustIDOnlineLookedUpAt timestamp. Default: skip already-looked-up.
	Force bool `json:"force,omitempty"`
	// Limit caps the number of candidates processed in this run. Used by
	// the nightly maintenance task to stay within a maintenance window
	// (e.g. process 8000 files per night ≈ 1 hour at 400ms throttle).
	// 0 = no cap.
	Limit int `json:"limit,omitempty"`
}

func (p *Plugin) onlineLookupDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.lookup-online",
		Liveness:        sdk.LivenessManual,
		Plugin:          "acoustid",
		DisplayName:     "Look up fingerprints on AcoustID.org",
		Description:     onlineLookupHelp,
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "acoustid.online",
		Cancellable:     true,
		Timeout:         24 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runOnlineLookup,
	}
}

// onlineLookupPrintUsable reports whether f's stored whole-file print may be
// sent to AcoustID: only prints written under the current encoding
// (fingerprint.PrintEncodingVersion). Legacy rows hold misdecoded bytes.
func onlineLookupPrintUsable(f *database.BookFileCore) bool {
	return f.HasCurrentPrint()
}

func (p *Plugin) runOnlineLookup(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	_, err := onlineLookupCore(ctx, params, p.headOnlineLookupEnv(reporter))
	return err
}

// headOnlineLookupEnv is HEAD's env: the plugin's store for reads and the write,
// and HEAD's own config and client. A plugin with no store hands the core a nil
// Store, which it refuses with HEAD's message.
func (p *Plugin) headOnlineLookupEnv(reporter sdk.Reporter) onlineLookupEnv {
	env := onlineLookupEnv{kindEnv: headEnv(reporter, true), Lookup: LookupDeps{}.hooks()}
	if p.store != nil {
		env.Store = p.store
		env.Writer = headKindWriter{store: p.store}
	}
	return env
}

// onlineLookupCore is the body of runOnlineLookup, shared by both shells. params
// is the raw start params, decoded here (after the store and API-key checks, as
// HEAD ordered them).
func onlineLookupCore(ctx context.Context, params json.RawMessage, env onlineLookupEnv) (onlineLookupResult, error) {
	if env.Store == nil {
		return onlineLookupResult{}, fmt.Errorf("database store not available")
	}

	// The persisted setting (set via PUT /api/v1/config from the AcousticDedup
	// tab) and the ACOUSTID_API_KEY env var both flow into this one field via
	// viper.BindEnv (see InitConfig) — no separate env fallback needed here.
	apiKey := env.Lookup.APIKey()
	if apiKey == "" {
		return onlineLookupResult{}, fmt.Errorf("AcoustID API key is not configured; set it on the AcoustID Dedup page or via the ACOUSTID_API_KEY env var")
	}
	lookup := env.Lookup.NewLookup(apiKey)

	var req OnlineLookupParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return onlineLookupResult{}, fmt.Errorf("parse params: %w", err)
		}
	}

	log := env.Logger

	prog := newProgress(env.kindEnv, 0)
	prog.Start("Loading book files for online lookup…")

	files, err := env.Store.GetAllBookFilesCore()
	if err != nil {
		return onlineLookupResult{}, fmt.Errorf("load book files: %w", err)
	}

	// Filter to candidates: must have a whole-file fp, must not already
	// be looked up (unless Force).
	//
	// The gate uses AcoustIDFingerprintDurationSec (>0 ⇒ a whole-file
	// fingerprint was computed) rather than len(AcoustIDFingerprint)==0,
	// because GetAllBookFilesCore returns BookFileCore — the raw
	// AcoustIDFingerprint blob is a heavy field that never exists on Core
	// (stripped under memdb, and not projected under Pebble-direct either).
	// DurationSec is the KEPT proxy field. The raw bytes themselves are
	// hydrated per-candidate at API-call time below (this loop is already
	// network-bound and rate-limited, so a per-candidate Pebble read is
	// negligible).
	type candidate struct {
		idx int // original index in files
		dur int
	}
	cands := make([]candidate, 0, 8192)
	needsRefingerprint := 0
	for i := range files {
		f := &files[i]
		dur := int(f.AcoustIDFingerprintDurationSec)
		if dur > 0 && !onlineLookupPrintUsable(f) {
			// Legacy-era print: not a valid AcoustID submission. Sending it
			// would return "no match" and stamping AcoustIDOnlineLookedUpAt
			// would then skip the file forever. Count it; the nightly
			// acoustid.backfill re-fingerprints it and clears the stamp.
			needsRefingerprint++
			continue
		}
		if dur <= 0 {
			// No whole-file fingerprint recorded for this file. NOTE: under
			// memdb the raw blob is stripped, so DurationSec is the ONLY
			// surviving "has whole-file fp" signal. This assumes the invariant
			// "AcoustIDFingerprint set ⇒ AcoustIDFingerprintDurationSec > 0"
			// (backfill.go writes both together). A historical row with a blob
			// but DurationSec==0 would be silently skipped here — see the
			// follow-up in TODO.md (verify with a prod query) before relying on
			// this for a completeness-critical sweep.
			continue
		}
		if !req.Force && f.AcoustIDOnlineLookedUpAt != nil {
			continue
		}
		if dur < 30 {
			// Sub-30s fingerprints won't survive AcoustID's matching;
			// don't burn quota on them.
			continue
		}
		cands = append(cands, candidate{idx: i, dur: dur})
	}

	if req.Limit > 0 && len(cands) > req.Limit {
		cands = cands[:req.Limit]
	}
	total := len(cands)
	if total == 0 {
		prog = newProgress(env.kindEnv, 0)
		prog.Start("No book files eligible for online lookup")
		prog.Done(fmt.Sprintf("Nothing to do — needs_refingerprint=%d (legacy-era prints; the nightly acoustid.backfill re-fingerprints them automatically); every other fingerprinted file already has an AcoustID result or no fingerprint exists.", needsRefingerprint))
		return onlineLookupResult{NeedsRefingerprint: needsRefingerprint}, nil
	}

	prog = newProgress(env.kindEnv, total)
	prog.Start(fmt.Sprintf("Querying acoustid.org for %d file(s)…", total))

	var matched, noMatch, failed, rateLimited int
	throttle := onlineLookupThrottleMin
	startedAt := time.Now()

	for i, c := range cands {
		select {
		case <-ctx.Done():
			return onlineLookupResult{}, ctx.Err()
		default:
		}

		f := &files[c.idx]

		// Hydrate the full row (raw whole-file fingerprint + writeback
		// target) at API-call time. BookFileCore never carries
		// AcoustIDFingerprint (heavy field, not on Core at all); GetBookFiles
		// reads raw Pebble unconditionally (no UseMemDB check), so it always
		// returns the real bytes. The hydrated *BookFile is also reused below
		// as the UpdateBookFile target — writing back the Core-derived
		// candidate would compile-error (no such method) or, worse, wipe
		// fingerprint fields if hand-reconstructed; see
		// docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md.
		var raw []byte
		var hydrated *database.BookFile
		var hydrateErr error
		if bookFiles, herr := env.Store.GetBookFiles(f.BookID); herr != nil {
			hydrateErr = fmt.Errorf("hydrate GetBookFiles: %w", herr)
		} else {
			for j := range bookFiles {
				if bookFiles[j].ID == f.ID {
					hydrated = &bookFiles[j]
					raw = hydrated.AcoustIDFingerprint
					break
				}
			}
			if hydrated == nil || len(raw) == 0 {
				// Data drift: the DurationSec proxy claimed a fingerprint
				// exists but the raw row has no blob either (or the row
				// disappeared between the slim scan and this hydrate).
				hydrateErr = fmt.Errorf("hydrate: no fingerprint bytes for file %s (data drift)", f.ID)
			}
		}

		var res acoustid.LookupResult
		var lerr error
		if hydrateErr != nil {
			// Skip the API call rather than send empty bytes; count it the
			// same as any other failed lookup so the run's accounting stays
			// honest and the file gets retried on the next pass.
			lerr = hydrateErr
		} else {
			// AcoustID's lookup takes Chromaprint's COMPRESSED form (what
			// fpcalc prints). Until 2026-09-19 this sent
			// EncodeWholeFingerprint (the app's uncompressed storage form),
			// which the service cannot match. Misaligned bytes are counted
			// as a failed lookup, never sent.
			fpStr, encErr := fingerprint.EncodeCompressedFingerprint(raw)
			if encErr != nil {
				lerr = encErr
			} else {
				res, lerr = lookup(ctx, fpStr, c.dur)
			}
		}
		now := time.Now().UTC()
		if lerr != nil {
			failed++
			if errors.Is(lerr, acoustid.ErrRateLimited) {
				// API limiter told us off — slow down for the rest of the
				// run. The client already retried 3× with Retry-After
				// before bubbling this up, so the only honest response is
				// to widen our own throttle.
				rateLimited++
				if throttle < onlineLookupThrottleMax {
					throttle = onlineLookupThrottleMax
					log.Warn("acoustid online lookup: rate-limited, widening throttle",
						"new_throttle_ms", throttle.Milliseconds())
				}
			}
			log.Warn("acoustid online lookup: request failed",
				"file_id", f.ID, "err", lerr)
		} else {
			// Mutate + write back the hydrated FULL row (not the Core
			// candidate) so fingerprint fields already on the Pebble row
			// survive the update. See the hydrate block above.
			hydrated.AcoustIDOnlineLookedUpAt = &now
			if res.Score >= AcoustIDOnlineMinScore && res.RecordingID != "" {
				hydrated.AcoustIDOnlineRecordingID = res.RecordingID
				hydrated.AcoustIDOnlineScore = res.Score
				matched++
				log.Info("acoustid online lookup: matched",
					"file_id", f.ID,
					"book_id", f.BookID,
					"recording_id", res.RecordingID,
					"score", fmt.Sprintf("%.3f", res.Score))
			} else {
				// Persist the looked-up timestamp so we don't re-query
				// every run; clear any stale match below threshold.
				hydrated.AcoustIDOnlineRecordingID = ""
				hydrated.AcoustIDOnlineScore = 0
				noMatch++
			}
			if uerr := env.Writer.UpdateBookFile(hydrated.ID, hydrated); uerr != nil {
				log.Warn("acoustid online lookup: persist failed",
					"file_id", f.ID, "err", uerr)
			}
		}

		// Heartbeat per file so the registry watchdog (5-min idle) never
		// kills this op, even on slow API responses.
		prog.StepN(i+1,
			fmt.Sprintf("Looking up %d/%d (matched=%d no_match=%d failed=%d)",
				i+1, total, matched, noMatch, failed))

		// Respect AcoustID's 3 req/sec free-tier limit. `throttle` widens
		// to onlineLookupThrottleMax after the first 429.
		if err := env.Lookup.Wait(ctx, throttle); err != nil {
			return onlineLookupResult{}, err
		}
	}

	prog.Done(fmt.Sprintf("Online lookup complete in %s — matched=%d no_match=%d failed=%d rate_limited=%d needs_refingerprint=%d (of %d files)",
		time.Since(startedAt).Round(time.Second),
		matched, noMatch, failed, rateLimited, needsRefingerprint, total))
	return onlineLookupResult{
		Total: total, Matched: matched, NoMatch: noMatch, Failed: failed,
		RateLimited: rateLimited, NeedsRefingerprint: needsRefingerprint,
	}, nil
}
