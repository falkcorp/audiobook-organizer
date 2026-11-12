// file: core/tasks/runview_redactor.go
// version: 1.0.0
// guid: aba34e3c-c895-4d96-b194-68e0e676fe4d
// last-edited: 2026-11-11

package tasks

import (
	"encoding/json"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
)

// RunViewRedactor adapts RedactRunView to events.RedactFunc: it decodes the
// payload as a RunView, applies RedactRunView with the subscriber's checker
// and encodes the result, so the realtime stream and the GET /runs serialiser
// run one redaction and never two copies of its rule. A payload that does not
// decode as a RunView is returned blanked to "{}": a payload the rule cannot
// read is a payload it cannot vouch for, and the stream must not carry it
// unredacted. It is the Redact value of the core.task.run-changed.v1 catalog
// entry (05-events §5E.3.1; COR3-13). A nil checker blanks, as RedactRunView
// does.
func RunViewRedactor() events.RedactFunc {
	return func(who rbac.Checker, payload json.RawMessage) json.RawMessage {
		var v RunView
		if err := json.Unmarshal(payload, &v); err != nil {
			return json.RawMessage("{}")
		}
		out, err := json.Marshal(RedactRunView(who, v))
		if err != nil {
			return json.RawMessage("{}")
		}
		return out
	}
}
