// file: internal/operations/opmode/dryrun.go
// version: 1.1.0
// guid: ea2e7a99-5038-479b-a7f2-19a6153feecd
// last-edited: 2026-09-25

// Package opmode resolves the preview/live mode of an operation from its params.
//
// Owner decision 2026-09-25: every operation that writes runs as a PREVIEW
// (dry run) when its request does not state a mode. A live run happens only when
// the caller says so explicitly, with dry_run=false (or its camelCase alias
// dryRun=false). Automated callers that must stay live pass the flag themselves.
//
// The failure this package exists to prevent is the plain `DryRun bool` param
// field: Go's zero value for bool is false, so a caller that omitted the key —
// a scheduler entry, a requeue with nil params, a script, a UI button that
// forgot the key — got a real mutation behind an identical "queued" response.
// A *bool keeps "omitted" distinguishable from "false", and ResolveDryRun maps
// omitted to true.
//
// Both spellings are accepted because both are in use across the codebase and
// a misspelled key must never silently pick a mode. Sending both with different
// values is an error rather than a guess.
//
// This is a leaf package on purpose: it imports only encoding/json and fmt, so
// any op in any layer (plugins, scheduler, server) can use it without an import
// cycle. internal/operations/opmode/guard_test.go enforces that no op params
// struct reintroduces a plain `bool` dry_run field.
package opmode

import (
	"encoding/json"
	"fmt"
)

// DryRunParams is the standard params shape of an op whose only params are its
// mode. Ops with other params declare the same two fields on their own struct
// and call ResolveDryRun.
type DryRunParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// ResolveDryRun returns the effective dry-run flag given the snake_case
// (dry_run) and camelCase (dryRun) values an op decoded. Either may be nil.
//
//   - neither sent  -> true (preview)
//   - one sent      -> that value
//   - both, agreeing -> that value
//   - both, disagreeing -> true and an error naming opID
//
// The error return is always paired with true, so a caller that ignores the
// error still fails toward preview.
func ResolveDryRun(opID string, snake, camel *bool) (bool, error) {
	return ResolveDryRunDefault(opID, snake, camel, true)
}

// ResolveDryRunDefault is ResolveDryRun with the omitted-mode value supplied by
// the caller. It exists for the maintenance-job family, whose fallback is the
// dry_run each job ADVERTISES in DefaultParams: true for every job with a
// preview mode, false only for the read-only jobs whose Run ignores the flag
// (internal/maintenance/jobs/preview_default_guard_test.go names them). Every
// other op uses ResolveDryRun.
//
// A disagreement is an error paired with true, as in ResolveDryRun, whatever
// the default: a caller that ignores the error still fails toward preview.
func ResolveDryRunDefault(opID string, snake, camel *bool, omitted bool) (bool, error) {
	if snake != nil && camel != nil && *snake != *camel {
		return true, fmt.Errorf("%s: dry_run=%v and dryRun=%v disagree; send one", opID, *snake, *camel)
	}
	switch {
	case snake != nil:
		return *snake, nil
	case camel != nil:
		return *camel, nil
	}
	return omitted, nil
}

// ParseDryRun decodes raw as DryRunParams and resolves it with ResolveDryRun.
// Empty raw is a preview. A malformed body is refused (with true), never
// treated as "no mode stated": a truncated or mistyped body must not run.
func ParseDryRun(opID string, raw json.RawMessage) (bool, error) {
	var p DryRunParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return true, fmt.Errorf("%s: invalid params: %w", opID, err)
		}
	}
	return ResolveDryRun(opID, p.DryRun, p.DryRunCamel)
}

// Live returns a pointer to false: the explicit live flag an automated caller
// sets on a params struct so its run does not fall to the preview default.
func Live() *bool {
	f := false
	return &f
}

// Preview returns a pointer to true.
func Preview() *bool {
	t := true
	return &t
}
