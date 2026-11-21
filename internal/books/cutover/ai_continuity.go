// file: internal/books/cutover/ai_continuity.go
// version: 2.0.0
// guid: 5d0e8a73-21c4-4f6b-9a57-3b8c1e6f2d94
// last-edited: 2026-11-20

package cutover

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// aiContinuityIDs are the rows whose capability ticks say which backend the
// legacy single-backend path served: the local OpenAI-compatible row and the
// OpenAI row. Whisper rows are not part of that path (their sites were routed
// whatever ai_endpoints_routing said).
var aiContinuityIDs = []string{headAIEndpointIDLocalLLM, headAIEndpointIDOpenAI}

// aiLegacyServed is the set of capabilities HEAD's legacy path served for an
// install with ai_endpoints_routing off, over the effective config cfg: the
// sites ai_endpoints_routing gates, in the mode that used the legacy backend.
//
//   - llm.filename_parse: the scanner parsed with the legacy local parser or the
//     openai-fallback-local chain when enable_ai_parsing was on and llm_mode was
//     local or openai-fallback-local (scanner newAIParserUnjournalled). Explicit
//     openai mode was never routed, and neither was a disabled one.
//   - embed.text: the embedding client's local branch (embedding_mode local) was
//     routed to the pool only when the switch was on; the openai modes keep
//     their legacy path whatever the switch says.
//
// Cover vision (llm.cover_art_vision) is not here on purpose: with the switch
// off the cover reader refused to run (RoutedCoverTextReader.Capacity is 0), so
// HEAD served nothing for it and the flip must not start paid vision calls.
// Every other capability's site either ignored the switch or was never served
// by the legacy path.
func aiLegacyServed(cfg *headConfig) []string {
	var served []string
	switch headEffectiveLLMMode(cfg) {
	case headAIBackendModeLocal, headAIBackendModeOpenAIFallbackLocal:
		if cfg.EnableAIParsing {
			served = append(served, headCapLLMFilenameParse)
		}
	}
	if headEffectiveEmbeddingMode(cfg) == headAIBackendModeLocal {
		served = append(served, headCapEmbedText)
	}
	return served
}

// aiRoutingContinuity keeps every feature HEAD served through its legacy AI
// path served after the flip forces routing on (05-ai section 10: one path,
// the router over the ai_endpoints rows).
//
// HEAD's ai_endpoints_routing defaulted to false. With it false the scanner's
// filename parse and the local embedding client ignored the stored rows and
// called the one backend ai_backend.* and the OpenAI key named. HEAD seeded the
// rows only when the stored list was absent, so an install whose list already
// existed (whisper servers added by hand, an OpenAI key entered after the first
// seed, a list edited while routing was off) can hold rows that do not tick
// what the legacy path was doing. Routed, that feature would find no endpoint
// and silently stop.
//
// tree is the planner's effective tree (the blob after HEAD's transforms and
// the config.yaml gap-fill). The rows the seed would derive from it (the same
// derivation, over HEAD's defaults plus the tree) are reduced to the
// capabilities aiLegacyServed says the legacy path served, and merged into the
// stored list: a missing local or OpenAI row is added with exactly those ticks,
// a stored one gains the missing ticks, and nothing else on a stored row is
// changed (URL, models, priority, features and any field this build does not
// know are left alone). A disabled row the legacy path was serving is enabled
// only when that cannot start anything else, because the flag did nothing while
// routing was off and enabling it also makes every other tick on the row live;
// otherwise it stays disabled and the report says so. With routing on in HEAD
// the stored rows were already the truth and nothing is touched; an install
// HEAD seeded itself derives the same rows, so the merge changes nothing there.
//
// It returns the cutover report lines (rows and capabilities, never a value)
// and edits tree["ai_endpoints"] in place.
func aiRoutingContinuity(tree map[string]any, hasKey bool) []string {
	list, ok := tree["ai_endpoints"].([]any)
	if !ok {
		return nil
	}
	enc, err := json.Marshal(tree)
	if err != nil {
		return nil
	}
	cfg, hasKey, ok := headSeedConfig(string(enc), headAIEndpointsSeed{HasOpenAIKey: hasKey})
	if !ok || cfg.AIEndpointsRouting {
		return nil
	}
	served := aiLegacyServed(cfg)
	if len(served) == 0 {
		return nil
	}

	var report []string
	for _, d := range headBuildMigratedAIEndpoints(cfg, hasKey) {
		if !slices.Contains(aiContinuityIDs, d.ID) {
			continue
		}
		var want []string
		for _, c := range d.Capabilities {
			if slices.Contains(served, c) {
				want = append(want, c)
			}
		}
		if len(want) == 0 {
			continue
		}
		var row map[string]any
		for _, r := range list {
			if m, ok := r.(map[string]any); ok && m["id"] == d.ID {
				row = m
			}
		}
		if row == nil {
			d.Capabilities = want
			added, err := decodeRow(d)
			if err != nil {
				continue
			}
			list = append(list, added)
			report = append(report, "ai_endpoints: routing was off, so the legacy path served "+d.ID+
				" and the stored list had no such row: added it (ticks "+strings.Join(want, ", ")+")")
			continue
		}
		caps := stringList(row["capabilities"])
		var added []string
		for _, c := range want {
			if !slices.Contains(caps, c) {
				caps = append(caps, c)
				added = append(added, c)
			}
		}
		row["capabilities"] = anyList(caps)
		line := ""
		if len(added) > 0 {
			line = " ticked " + strings.Join(added, ", ")
		}
		if enabled, _ := row["enabled"].(bool); !enabled {
			if extra := without(caps, served); len(extra) == 0 {
				row["enabled"] = true
				line += " and enabled the row"
			} else {
				line += " (the row is disabled and stays disabled: enabling it would also start " +
					strings.Join(extra, ", ") + ", which the legacy path did not serve; enable it in Settings to have " +
					strings.Join(want, ", ") + " served)"
			}
		}
		if line != "" {
			report = append(report, "ai_endpoints: routing was off, so the legacy path served "+d.ID+" beyond what its row ticked:"+line)
		}
	}
	if len(report) == 0 {
		return nil
	}
	tree["ai_endpoints"] = list
	return report
}

// decodeRow is a derived row as a tree member (a JSON object with json.Number
// values, the shape decodeTree gives every member).
func decodeRow(d headAIEndpoint) (map[string]any, error) {
	enc, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(enc))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func stringList(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, e := range l {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func anyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// without is the members of have that are not in allowed.
func without(have, allowed []string) []string {
	var out []string
	for _, h := range have {
		if !slices.Contains(allowed, h) {
			out = append(out, h)
		}
	}
	return out
}
