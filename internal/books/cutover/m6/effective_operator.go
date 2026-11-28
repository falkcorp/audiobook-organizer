// file: internal/books/cutover/m6/effective_operator.go
// version: 1.0.0
// guid: 6ec0e669-18a2-4679-b9ee-6eceba3b1cbc
// last-edited: 2026-11-28

package m6

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
)

// operatorURLPaths is the closed set of operator-layer paths that carry an
// integration's URL setting. It is the whole of what Config.Effective can
// answer, because HEAD's environment and config file supplied exactly two URLs
// (OPENAI_BASE_URL and the Deluge web URL).
//
// A path is integrations.<kind>.<instance>.<field>, and the overlay is keyed by
// its last segment, the way core keys it (core/integrations opPaths): the
// segment IS the adapter's settings field. That is why the openai path is
// integrations.ai.openai.url and not base_url: the rename table retargets HEAD's
// openai_base_url and the OPENAI_BASE_URL binding onto the adapter's field name,
// so a value an operator supplies lands in the field core overlays and M6 signs.
//
// This is a table, not a derivation from the rename table: that table marks the
// email host and port as destinations too (they are a host-and-port pair, not
// URL settings), and cannot be imported from here (cutover imports this
// package). The pins run the other way round: the m6_test pins walk the rename
// table and the env bindings, require every passthrough path to be mapped here
// or listed in notURLPaths, and require each mapped path to be a URL-typed field
// of its adapter's Settings.
var operatorURLPaths = map[config.Path]struct{}{
	"integrations.ai.openai.url":       {},
	"integrations.download.deluge.url": {},
}

// operatorField splits an operator path into the instance and the settings
// field, as core's opPaths does: four dot-separated segments, the last being the
// field. ok is false for any other shape.
func operatorField(p config.Path) (ref integrations.Ref, field string, ok bool) {
	segs := strings.Split(string(p), ".")
	if len(segs) != 4 || segs[0] != "integrations" {
		return integrations.Ref{}, "", false
	}
	return integrations.Ref{Kind: integrations.Kind(segs[1]), Instance: integrations.InstanceID(segs[2])}, segs[3], true
}

// notURLPaths are the integrations passthrough paths that are deliberately not
// URL settings, so Effective has nothing to say about them: the email host and
// port are a host-and-port pair (core signs their origin from the pair, not from
// a URL), and the username and from address are plain strings.
var notURLPaths = map[config.Path]string{
	"integrations.notify.email.host":     "host of the host-and-port pair, not a URL",
	"integrations.notify.email.port":     "port of the host-and-port pair, not a URL",
	"integrations.notify.email.username": "plain string",
	"integrations.notify.email.from":     "plain string",
}

// EffectiveFromPassthrough builds Config.Effective from the operator layer's
// passthrough values (core's ConfigReads.PassthroughValues: raw values keyed by
// integrations.<kind>.<instance>.<field>, supplied by the environment, a flag or
// the config file). The answer for a ref is the operator-supplied URL settings,
// settings field to URL as supplied: the overlay core applies over the stored
// row, so the origins M6 signs are the ones core computes at its first boot.
//
// The values are read once, when the function is built, because the cutover
// answers from one snapshot of the operator layer; a single-use iterator is
// safe, and a nil iterator is no values.
//
// A ref with no operator URL gets a nil map, never an empty one. An empty or
// whitespace-only value, and JSON null, is no overlay for that field (which is
// how a stored deluge URL falls back to the blob). A path that is not in
// operatorURLPaths is ignored: the passthrough layer holds other settings (the
// email host, a model name) that are not URLs.
//
// A value that is not a JSON string is an error for its ref and no other: an
// operator URL that is a number or an object is a mistake the cutover must not
// paper over by signing trust from the stored URL. The error names the path and
// the layer that supplied it (OperatorValue.By), never the value.
func EffectiveFromPassthrough(values iter.Seq2[config.Path, config.OperatorValue]) func(ctx context.Context, ref integrations.Ref) (map[string]string, error) {
	urls := map[integrations.Ref]map[string]string{}
	bad := map[integrations.Ref]error{}
	if values != nil {
		for p, v := range values {
			if _, ok := operatorURLPaths[p]; !ok {
				continue
			}
			ref, field, ok := operatorField(p)
			if !ok {
				continue
			}
			var s string
			if err := json.Unmarshal(v.Value, &s); err != nil {
				if _, seen := bad[ref]; !seen {
					bad[ref] = fmt.Errorf("m6: the operator value %s (from %s) is not a string URL", p, v.By)
				}
				continue
			}
			if strings.TrimSpace(s) == "" {
				continue
			}
			if urls[ref] == nil {
				urls[ref] = map[string]string{}
			}
			urls[ref][field] = s
		}
	}
	return func(_ context.Context, ref integrations.Ref) (map[string]string, error) {
		if err := bad[ref]; err != nil {
			return nil, err
		}
		if len(urls[ref]) == 0 {
			return nil, nil
		}
		return maps.Clone(urls[ref]), nil
	}
}
