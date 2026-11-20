// file: internal/books/cutover/ai_continuity.go
// version: 1.0.0
// guid: 5d0e8a73-21c4-4f6b-9a57-3b8c1e6f2d94
// last-edited: 2026-11-20

package cutover

import (
	"encoding/json"
	"slices"
	"strings"
)

// aiContinuityIDs are the rows whose capability ticks say which backend the
// legacy single-backend path served: the local OpenAI-compatible row and the
// OpenAI row. Whisper rows are not part of that path (their sites were routed
// whatever ai_endpoints_routing said).
var aiContinuityIDs = []string{headAIEndpointIDLocalLLM, headAIEndpointIDOpenAI}

// aiRoutingContinuity keeps every feature HEAD served through its legacy AI
// path served after the flip forces routing on (05-ai section 10: one path,
// the router over the ai_endpoints rows).
//
// HEAD's ai_endpoints_routing defaulted to false. With it false the scanner's
// filename parse, the local embedding client and the cover reader ignored the
// stored rows and called the one backend ai_backend.* and the OpenAI key named.
// HEAD seeded the rows only when the stored list was absent, so an install
// whose list already existed (whisper servers added by hand, an OpenAI key
// entered after the first seed, a list edited while routing was off) can hold
// rows that do not tick what the legacy path was doing. Routed, that feature
// would find no endpoint and silently stop.
//
// So when routing was off, the rows the seed would derive from the effective
// config are merged into the stored list: a missing local or OpenAI row is
// added, and a stored one gains the capability ticks the derivation holds
// (an existing tick, URL, model or priority is never changed; a disabled row
// the legacy path was serving is enabled, because the flag did nothing while
// routing was off). With routing on in HEAD the stored rows were already the
// truth and nothing is touched. An install HEAD seeded itself derives the same
// rows, so the merge changes nothing there.
//
// It returns the cutover report lines, naming rows and capabilities, never a
// value, and edits b.Fields["ai_endpoints"] and b.Config.AIEndpoints in place.
func aiRoutingContinuity(b *headBlob, hasKey bool) []string {
	if b.Config.AIEndpointsRouting {
		return nil
	}
	cfg := b.Config
	// A key the blob itself carries counts, as it does for the seed.
	hasKey = hasKey || cfg.OpenAIAPIKey != ""
	if hasKey && cfg.OpenAIAPIKey == "" {
		// The effective-mode helpers look at the key field itself.
		cfg.OpenAIAPIKey = "present"
	}
	var derived []headAIEndpoint
	for _, d := range headBuildMigratedAIEndpoints(&cfg, hasKey) {
		if slices.Contains(aiContinuityIDs, d.ID) {
			derived = append(derived, d)
		}
	}
	if len(derived) == 0 {
		return nil
	}

	rows := slices.Clone(b.Config.AIEndpoints)
	var report []string
	for _, d := range derived {
		i := slices.IndexFunc(rows, func(r headAIEndpoint) bool { return r.ID == d.ID })
		if i < 0 {
			rows = append(rows, d)
			report = append(report, "ai_endpoints: routing was off, so the legacy path served "+d.ID+
				" and the stored list had no such row: added it (ticks "+strings.Join(d.Capabilities, ", ")+")")
			continue
		}
		var added []string
		for _, c := range d.Capabilities {
			if !slices.Contains(rows[i].Capabilities, c) {
				rows[i].Capabilities = append(rows[i].Capabilities, c)
				added = append(added, c)
			}
		}
		enabled := !rows[i].Enabled && len(d.Capabilities) > 0
		if enabled {
			rows[i].Enabled = true
		}
		if len(added) == 0 && !enabled {
			continue
		}
		line := "ai_endpoints: routing was off, so the legacy path served " + d.ID + " beyond what its row ticked:"
		if len(added) > 0 {
			line += " ticked " + strings.Join(added, ", ")
		}
		if enabled {
			line += " and enabled the row"
		}
		report = append(report, line)
	}
	if len(report) == 0 {
		return nil
	}
	enc, err := json.Marshal(rows)
	if err != nil {
		return nil
	}
	b.Config.AIEndpoints = rows
	b.Fields["ai_endpoints"] = enc
	return report
}
