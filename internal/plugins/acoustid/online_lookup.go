// file: internal/plugins/acoustid/online_lookup.go
// version: 1.2.0
// guid: 6e7f8091-a2b3-c4d5-e6f7-08192a3b4c5d
// last-edited: 2026-07-06

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/acoustid"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

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
		Plugin:          "acoustid",
		DisplayName:     "Look up fingerprints on AcoustID.org",
		Description:     "For every BookFile with a stored whole-file chromaprint, queries the acoustid.org /v2/lookup API for a MusicBrainz recording match. Stores the top recording_id + score when score >= 0.85. Requires the ACOUSTID_API_KEY env var.",
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

func (p *Plugin) runOnlineLookup(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("database store not available")
	}

	// Prefer the persisted setting (set via PUT /api/v1/config from the
	// AcousticDedup tab). Fall back to env so the original env-only
	// setup still works.
	apiKey := config.AppConfig.AcoustIDAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("ACOUSTID_API_KEY")
	}
	if apiKey == "" {
		return fmt.Errorf("AcoustID API key is not configured; set it on the AcoustID Dedup page or via the ACOUSTID_API_KEY env var")
	}
	client := acoustid.NewClient(apiKey)

	var req OnlineLookupParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	log := reporter.Logger()

	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Loading book files for online lookup…")

	files, err := p.store.GetAllBookFiles()
	if err != nil {
		return fmt.Errorf("load book files: %w", err)
	}

	// Filter to candidates: must have a whole-file fp, must not already
	// be looked up (unless Force).
	//
	// The gate uses AcoustIDFingerprintDurationSec (>0 ⇒ a whole-file
	// fingerprint was computed) rather than len(AcoustIDFingerprint)==0,
	// because GetAllBookFiles returns the memdb-slim projection in
	// production (UseMemDB=true): stripBookFileForMemdb nils the raw
	// AcoustIDFingerprint blob to save RAM but keeps DurationSec. The old
	// byte-length gate was always true under memdb, so this op silently
	// found zero candidates in prod. The raw bytes themselves are hydrated
	// per-candidate at API-call time below (this loop is already
	// network-bound and rate-limited, so a per-candidate Pebble read is
	// negligible) — we deliberately do NOT stash `raw` from the slim struct
	// here.
	type candidate struct {
		idx int // original index in files
		dur int
	}
	cands := make([]candidate, 0, 8192)
	for i := range files {
		f := &files[i]
		dur := int(f.AcoustIDFingerprintDurationSec)
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
		prog = sdk.NewProgress(reporter, 0)
		prog.Start("No book files eligible for online lookup")
		prog.Done("Nothing to do — every fingerprinted file already has an AcoustID result or no fingerprint exists.")
		return nil
	}

	prog = sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Querying acoustid.org for %d file(s)…", total))

	var matched, noMatch, failed, rateLimited int
	throttle := onlineLookupThrottleMin
	startedAt := time.Now()

	for i, c := range cands {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		f := &files[c.idx]

		// Hydrate the raw whole-file fingerprint at API-call time.
		// GetAllBookFiles strips AcoustIDFingerprint under memdb
		// (stripBookFileForMemdb); the candidate filter above only checked
		// the memdb-safe DurationSec proxy, so most candidates land here
		// with a nil blob. GetBookFiles reads raw Pebble unconditionally
		// (no UseMemDB check), so it always returns the real bytes.
		raw := f.AcoustIDFingerprint
		var hydrateErr error
		if len(raw) == 0 {
			if bookFiles, herr := p.store.GetBookFiles(f.BookID); herr != nil {
				hydrateErr = fmt.Errorf("hydrate GetBookFiles: %w", herr)
			} else {
				for _, hf := range bookFiles {
					if hf.ID == f.ID {
						raw = hf.AcoustIDFingerprint
						break
					}
				}
				if len(raw) == 0 {
					// Data drift: the DurationSec proxy claimed a fingerprint
					// exists but the raw row has no blob either.
					hydrateErr = fmt.Errorf("hydrate: no fingerprint bytes for file %s (data drift)", f.ID)
				}
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
			// Encode raw bytes to the canonical chromaprint base64+header form
			// that AcoustID expects.
			fpStr := fingerprint.EncodeWholeFingerprint(raw)
			res, lerr = client.Lookup(ctx, fpStr, c.dur)
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
			f.AcoustIDOnlineLookedUpAt = &now
			if res.Score >= AcoustIDOnlineMinScore && res.RecordingID != "" {
				f.AcoustIDOnlineRecordingID = res.RecordingID
				f.AcoustIDOnlineScore = res.Score
				matched++
				log.Info("acoustid online lookup: matched",
					"file_id", f.ID,
					"book_id", f.BookID,
					"recording_id", res.RecordingID,
					"score", fmt.Sprintf("%.3f", res.Score))
			} else {
				// Persist the looked-up timestamp so we don't re-query
				// every run; clear any stale match below threshold.
				f.AcoustIDOnlineRecordingID = ""
				f.AcoustIDOnlineScore = 0
				noMatch++
			}
			if uerr := p.store.UpdateBookFile(f.ID, f); uerr != nil {
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
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(throttle):
		}
	}

	prog.Done(fmt.Sprintf("Online lookup complete in %s — matched=%d no_match=%d failed=%d rate_limited=%d (of %d files)",
		time.Since(startedAt).Round(time.Second),
		matched, noMatch, failed, rateLimited, total))
	return nil
}
