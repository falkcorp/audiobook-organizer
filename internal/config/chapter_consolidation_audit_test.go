// file: internal/config/chapter_consolidation_audit_test.go
// version: 1.0.0
// guid: b2c4b30a-8d16-4d7c-8a87-8e0ec63b51a2
// last-edited: 2026-08-27

package config

import "testing"

func TestExplicitChapterConsolidationDisableInBlob(t *testing.T) {
	tests := []struct {
		name string
		blob string
		cfg  Config
		want bool
	}{
		{
			name: "explicit zero disables consolidation",
			blob: `{"chapter_consolidation_threshold_min":0}`,
			cfg:  Config{ChapterConsolidationThresholdMin: 0},
			want: true,
		},
		{
			name: "missing key is not an explicit disable",
			blob: `{}`,
			cfg:  Config{ChapterConsolidationThresholdMin: 0},
			want: false,
		},
		{
			name: "positive value is enabled",
			blob: `{"chapter_consolidation_threshold_min":10}`,
			cfg:  Config{ChapterConsolidationThresholdMin: 10},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := explicitChapterConsolidationDisable(tt.blob, tt.cfg); got != tt.want {
				t.Fatalf("explicitChapterConsolidationDisable() = %t, want %t", got, tt.want)
			}
		})
	}
}
