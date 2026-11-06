// file: core/log/retention_test.go
// version: 1.0.0
// guid: a9175e03-87ae-4472-8752-47c3559a7e31
// last-edited: 2026-11-06

package log

import "testing"

func TestResolveLogRetentionDays(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{in: 0, want: DefaultLogRetentionDays},
		{in: 30, want: 30},
		{in: LogRetentionKeepForever, want: 0},
		{in: -7, want: 0},
	} {
		if got := ResolveLogRetentionDays(tc.in); got != tc.want {
			t.Errorf("ResolveLogRetentionDays(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
