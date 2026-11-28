// file: internal/activity/user_attr_test.go
// version: 0.1.0
// guid: 7b3e91c2-4d58-4f0a-a6c1-0e9d2f84b573
// last-edited: 2026-11-28

package activity

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NamesUserAttr is true exactly where WithoutUserAttrs would have removed or
// altered the attribute: the one key rule, applied at any depth.
func TestNamesUserAttr_MatchesWhatWithoutUserAttrsRemoves(t *testing.T) {
	rows := []struct {
		name string
		key  string
		raw  string // the attribute's value as the JSON the line stores
	}{
		{"numeric user id", "user_id", `42`},
		{"opaque owner", "owner", `"x9f3k2"`},
		{"count stays", "user_count", `3`},
		{"flag stays", "owner_only", `true`},
		{"account fails closed", "account", `7`},
		{"camelCase count fails closed", "userCount", `3`},
		{"clean text", "title", `"Year One"`},
		{"clean number", "book_count", `5`},
		{"user key in a neutral map", "request", `{"user_id":17}`},
		{"user key in a deeper map", "request", `{"inner":{"login":"batman"}}`},
		{"user key in a list", "rows", `[{"email_address":"bruce@example.com"}]`},
		{"count in a neutral map stays", "request", `{"user_count":2}`},
		{"clean map", "batch", `{"size":3}`},
		{"text naming a user", "note", `"user_id=42 logged in"`},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			var v any
			require.NoError(t, json.Unmarshal([]byte(r.raw), &v))
			line := `{"` + r.key + `":` + r.raw + `}`
			var before, after any
			require.NoError(t, json.Unmarshal([]byte(line), &before))
			require.NoError(t, json.Unmarshal([]byte(WithoutUserAttrs(line)), &after))
			redacted := !reflect.DeepEqual(before, after)
			assert.Equal(t, redacted, NamesUserAttr(r.key, v),
				"NamesUserAttr must agree with WithoutUserAttrs on %s", line)
		})
	}
}
