// file: internal/activity/user_text_test.go
// version: 1.0.0
// guid: b3f60c18-4d97-4e2a-8a51-7c9e0d2f5a46
// last-edited: 2026-10-26

package activity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// WithoutOthersUserText replaces text that names another user with the fixed
// notice and leaves everything else, including the empty string, alone. The
// shapes are the ones the log-line predicate catches (NamesOthersUserLog).
func TestWithoutOthersUserText(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want string
	}{
		"empty":                 {"", ""},
		"plain error":           {"scan of Year One failed: disk full", "scan of Year One failed: disk full"},
		"progress with counts":  {"scanning Knightfall 12/40", "scanning Knightfall 12/40"},
		"key=value user id":     {"merge failed: user_id=01INVENTEDOWNER0000000000A could not be moved", WithheldUserText},
		"user followed by id":   {"moving state for user 01INVENTEDOWNER0000000000A", WithheldUserText},
		"e-mail address":        {"invite to bruce.wayne@example.invalid bounced", WithheldUserText},
		"user-data subsystem":   {"abs: progress patch rejected", WithheldUserText},
		"merge subsystem":       {"merge: moved 3 rows for Hush", WithheldUserText},
		"user listening state":  {"could not restore user listening state", WithheldUserText},
		"word user, no account": {"the user cancelled the scan", "the user cancelled the scan"},
	} {
		assert.Equal(t, tc.want, WithoutOthersUserText(tc.in), name)
	}
}
