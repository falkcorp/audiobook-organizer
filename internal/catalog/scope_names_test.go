// file: internal/catalog/scope_names_test.go
// version: 1.0.0
// guid: 8b4e1c63-2d7f-4a95-b0e8-6f3a9c1d5e27
// last-edited: 2026-10-05

package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScopeNames(t *testing.T) {
	cases := []struct {
		name  string
		want  []string
		skip  string
		split bool
	}{
		{"Ann Leckie", []string{"Ann Leckie"}, "", false},
		{"Brian Herbert, Kevin J. Anderson", []string{"Brian Herbert, Kevin J. Anderson"}, "", false},
		{"Big Finish Productions", nil, scopeSkipJunk, false},
		{"Big Finish Production", nil, scopeSkipPublisher, false},
		{"Wraithmarked Studio", nil, scopeSkipPublisher, false},
		{"Jay Rubin - translator", nil, scopeSkipRole, false},
		{"Haruki Murakami, Jay Rubin - translator, Philip Gabriel - translator", []string{"Haruki Murakami"}, "", true},
		{"Cixin Liu, Ken Liu - translator", []string{"Cixin Liu"}, "", true},
		{"Various, Ken Liu - translator", nil, scopeSkipRole, false},
	}
	for _, c := range cases {
		got, skip, split := scopeNames(c.name)
		require.Equal(t, c.want, got, c.name)
		require.Equal(t, c.skip, skip, c.name)
		require.Equal(t, c.split, split, c.name)
	}
}
