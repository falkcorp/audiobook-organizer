// file: internal/operations/registry/preview_test.go
// version: 1.0.0
// guid: 4000c6f3-ba9e-43d9-b294-eb3b2b4b70a8
// last-edited: 2026-10-18

package registry

import (
	"strings"
	"testing"
)

type previewDryRunParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	Limit       int   `json:"limit"`
}

type previewApplyParams struct {
	Apply bool `json:"apply"`
}

type previewLiveParams struct {
	Live bool `json:"live"`
}

type previewCamelOnly struct {
	DryRun *bool `json:"dryRun"`
}

type previewStringApply struct {
	Apply string `json:"apply"`
}

type previewEmbedded struct {
	previewDryRunParams
	Other string `json:"other"`
}

type previewUntagged struct {
	DryRun bool
}

func TestPreviewSpec_Check(t *testing.T) {
	cases := []struct {
		name    string
		spec    PreviewSpec
		wantErr string
	}{
		{"zero is no preview, not an error", PreviewSpec{}, ""},
		{"dry_run pointer field", PreviewSpec{Key: PreviewDryRun, Params: previewDryRunParams{}}, ""},
		{"pointer to params", PreviewSpec{Key: PreviewDryRun, Params: &previewDryRunParams{}}, ""},
		{"apply bool field", PreviewSpec{Key: PreviewApply, Params: previewApplyParams{}}, ""},
		{"live bool field", PreviewSpec{Key: PreviewLive, Params: previewLiveParams{}}, ""},
		{"embedded struct is flattened", PreviewSpec{Key: PreviewDryRun, Params: previewEmbedded{}}, ""},
		{"key without params", PreviewSpec{Key: PreviewDryRun}, "without the params type"},
		{"params without key", PreviewSpec{Params: previewApplyParams{}}, "is not"},
		{"unknown key", PreviewSpec{Key: "commit", Params: previewApplyParams{}}, "is not"},
		{"camelCase only is not dry_run", PreviewSpec{Key: PreviewDryRun, Params: previewCamelOnly{}}, "no field tagged"},
		{"missing apply", PreviewSpec{Key: PreviewApply, Params: previewDryRunParams{}}, "no field tagged"},
		{"apply not bool", PreviewSpec{Key: PreviewApply, Params: previewStringApply{}}, "not bool"},
		{"untagged field name is not the key", PreviewSpec{Key: PreviewDryRun, Params: previewUntagged{}}, "no field tagged"},
		{"not a struct", PreviewSpec{Key: PreviewApply, Params: map[string]any{}}, "not a struct"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Check()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestPreviewSpec_HasJSONField(t *testing.T) {
	s := PreviewSpec{Key: PreviewDryRun, Params: previewEmbedded{}}
	for _, k := range []string{"dry_run", "dryRun", "limit", "other"} {
		if !s.HasJSONField(k) {
			t.Errorf("HasJSONField(%q) = false, want true", k)
		}
	}
	if s.HasJSONField("apply") {
		t.Error("HasJSONField(apply) = true on a struct without it")
	}
	if (PreviewSpec{}).HasJSONField("dry_run") {
		t.Error("an undeclared spec has no fields")
	}
}
