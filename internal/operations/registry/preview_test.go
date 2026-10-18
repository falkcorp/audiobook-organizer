// file: internal/operations/registry/preview_test.go
// version: 2.0.0
// guid: 4000c6f3-ba9e-43d9-b294-eb3b2b4b70a8
// last-edited: 2026-10-18

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type previewDryRunParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	Limit       int   `json:"limit"`
}

type previewSnakeOnly struct {
	DryRun *bool `json:"dry_run,omitempty"`
}

type previewApplyParams struct {
	Apply bool `json:"apply"`
}

type previewLiveParams struct {
	Live bool `json:"live"`
}

// previewTwoFamilies is the vg-primary-repair / repoint-version-primary shape:
// a write needs apply && !dry_run.
type previewTwoFamilies struct {
	Apply  bool `json:"apply"`
	DryRun bool `json:"dry_run"`
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

type previewCaseVariant struct {
	Apply bool `json:"APPLY"`
}

type previewNoModeKey struct {
	Limit int `json:"limit"`
}

type previewOneOf struct {
	Apply    bool     `json:"apply"`
	Rollback bool     `json:"rollback"`
	Repair   bool     `json:"repair"`
	GroupIDs []string `json:"group_ids"`
}

func TestPreviewSpec_Check(t *testing.T) {
	cases := []struct {
		name    string
		spec    PreviewSpec
		wantErr string
	}{
		{"zero is no preview, not an error", PreviewSpec{}, ""},
		{"dry_run and dryRun pointer fields", PreviewSpec{Params: previewDryRunParams{}}, ""},
		{"pointer to params", PreviewSpec{Params: &previewDryRunParams{}}, ""},
		{"apply bool field", PreviewSpec{Params: previewApplyParams{}}, ""},
		{"live bool field", PreviewSpec{Params: previewLiveParams{}}, ""},
		{"two families", PreviewSpec{Params: previewTwoFamilies{}}, ""},
		{"embedded struct is flattened", PreviewSpec{Params: previewEmbedded{}}, ""},
		{"LiveOneOf alone", PreviewSpec{Params: previewOneOf{}, LiveOneOf: []string{"apply", "rollback", "repair"}}, ""},
		{"LiveOneOf without params", PreviewSpec{LiveOneOf: []string{"apply"}}, "without the params type"},
		{"StartCheck without params", PreviewSpec{StartCheck: func(map[string]json.RawMessage, bool) error { return nil }}, "without the params type"},
		{"no mode key", PreviewSpec{Params: previewNoModeKey{}}, "decodes none"},
		{"mode key not bool", PreviewSpec{Params: previewStringApply{}}, "not bool"},
		{"untagged field differs only in case", PreviewSpec{Params: previewUntagged{}}, "only in case"},
		{"tag differs only in case", PreviewSpec{Params: previewCaseVariant{}}, "only in case"},
		{"not a struct", PreviewSpec{Params: map[string]any{}}, "not a struct"},
		{"LiveOneOf field missing", PreviewSpec{Params: previewOneOf{}, LiveOneOf: []string{"apply", "nuke"}}, `no field tagged "nuke"`},
		{"LiveOneOf duplicate", PreviewSpec{Params: previewOneOf{}, LiveOneOf: []string{"apply", "apply"}}, "twice"},
		{"LiveOneOf dry_run", PreviewSpec{Params: previewDryRunParams{}, LiveOneOf: []string{"dry_run"}}, "writes when false"},
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

func TestPreviewSpec_ModeKeysAreReadOffTheType(t *testing.T) {
	cases := []struct {
		spec     PreviewSpec
		keys     []string
		families []string
	}{
		{PreviewSpec{Params: previewDryRunParams{}}, []string{"dry_run", "dryRun"}, []string{"dry_run"}},
		{PreviewSpec{Params: previewEmbedded{}}, []string{"dry_run", "dryRun"}, []string{"dry_run"}},
		{PreviewSpec{Params: previewApplyParams{}}, []string{"apply"}, []string{"apply"}},
		{PreviewSpec{Params: previewTwoFamilies{}}, []string{"dry_run", "apply"}, []string{"dry_run", "apply"}},
		{PreviewSpec{Params: previewOneOf{}, LiveOneOf: []string{"apply", "rollback", "repair"}}, nil, nil},
		{PreviewSpec{}, nil, nil},
	}
	for _, tc := range cases {
		if got := tc.spec.ModeKeys(); !reflect.DeepEqual(got, tc.keys) {
			t.Errorf("%T ModeKeys = %v, want %v", tc.spec.Params, got, tc.keys)
		}
		if got := tc.spec.ModeFamilies(); !reflect.DeepEqual(got, tc.families) {
			t.Errorf("%T ModeFamilies = %v, want %v", tc.spec.Params, got, tc.families)
		}
	}
	s := PreviewSpec{Params: previewEmbedded{}}
	for _, k := range []string{"dry_run", "dryRun", "limit", "other"} {
		if !s.Decodes(k) {
			t.Errorf("Decodes(%q) = false, want true", k)
		}
	}
	if s.Decodes("apply") || (PreviewSpec{}).Decodes("dry_run") {
		t.Error("Decodes reports a field the type does not have")
	}
}

func decodeMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("resolved params do not decode: %v (%s)", err, raw)
	}
	return m
}

func TestPreviewSpec_ResolveWritesEveryModeKey(t *testing.T) {
	cases := []struct {
		name   string
		spec   PreviewSpec
		params string
		mode   string
		want   map[string]any
	}{
		{"dry_run preview", PreviewSpec{Params: previewSnakeOnly{}}, `{}`, "preview", map[string]any{"dry_run": true}},
		{"dry_run live", PreviewSpec{Params: previewSnakeOnly{}}, `{}`, "live", map[string]any{"dry_run": false}},
		{"both spellings written", PreviewSpec{Params: previewDryRunParams{}}, `{"limit":3}`, "live",
			map[string]any{"dry_run": false, "dryRun": false, "limit": float64(3)}},
		{"apply preview", PreviewSpec{Params: previewApplyParams{}}, `{}`, "preview", map[string]any{"apply": false}},
		{"live live", PreviewSpec{Params: previewLiveParams{}}, `{}`, "live", map[string]any{"live": true}},
		{"two families preview", PreviewSpec{Params: previewTwoFamilies{}}, `{}`, "preview", map[string]any{"apply": false, "dry_run": true}},
		{"two families live", PreviewSpec{Params: previewTwoFamilies{}}, `{}`, "live", map[string]any{"apply": true, "dry_run": false}},
		{"omitted mode, no keys, is a preview", PreviewSpec{Params: previewTwoFamilies{}}, `{}`, "", map[string]any{"apply": false, "dry_run": true}},
		{"omitted mode follows a stated key", PreviewSpec{Params: previewTwoFamilies{}}, `{"apply":true}`, "", map[string]any{"apply": true, "dry_run": false}},
		{"omitted mode follows stated keys that agree", PreviewSpec{Params: previewTwoFamilies{}}, `{"apply":true,"dry_run":false}`, "", map[string]any{"apply": true, "dry_run": false}},
		{"LiveOneOf preview writes all false", PreviewSpec{Params: previewOneOf{}, LiveOneOf: []string{"apply", "rollback", "repair"}}, `{"group_ids":["g"]}`, "preview",
			map[string]any{"apply": false, "rollback": false, "repair": false, "group_ids": []any{"g"}}},
		{"LiveOneOf live keeps the caller's one switch", PreviewSpec{Params: previewOneOf{}, LiveOneOf: []string{"apply", "rollback", "repair"}}, `{"rollback":true}`, "live",
			map[string]any{"rollback": true}},
		{"LiveOneOf omitted mode follows a true switch", PreviewSpec{Params: previewOneOf{}, LiveOneOf: []string{"apply", "rollback", "repair"}}, `{"repair":true}`, "",
			map[string]any{"repair": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.spec.Resolve(json.RawMessage(tc.params), tc.mode)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := decodeMap(t, out); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Resolve(%s, %q) = %v, want %v", tc.params, tc.mode, got, tc.want)
			}
		})
	}
}

func TestPreviewSpec_ResolveRefuses(t *testing.T) {
	oneOf := PreviewSpec{Params: previewOneOf{}, LiveOneOf: []string{"apply", "rollback", "repair"}}
	cases := []struct {
		name    string
		spec    PreviewSpec
		params  string
		mode    string
		wantErr string
	}{
		{"undeclared", PreviewSpec{}, `{}`, "live", "no preview declared"},
		{"bad mode", PreviewSpec{Params: previewApplyParams{}}, `{}`, "commit", "mode must be"},
		{"not an object", PreviewSpec{Params: previewApplyParams{}}, `[1]`, "live", "JSON object"},
		{"mode contradicts key", PreviewSpec{Params: previewApplyParams{}}, `{"apply":true}`, "preview", "contradicts apply"},
		{"mode contradicts dry_run", PreviewSpec{Params: previewSnakeOnly{}}, `{"dry_run":true}`, "live", "contradicts dry_run"},
		{"keys disagree", PreviewSpec{Params: previewTwoFamilies{}}, `{"apply":true,"dry_run":true}`, "", "disagree"},
		{"spellings disagree", PreviewSpec{Params: previewDryRunParams{}}, `{"dry_run":true,"dryRun":false}`, "", "disagree"},
		{"key not bool", PreviewSpec{Params: previewApplyParams{}}, `{"apply":"yes"}`, "", "true or false"},
		{"key null", PreviewSpec{Params: previewApplyParams{}}, `{"apply":null}`, "", "true or false"},
		{"unread mode key", PreviewSpec{Params: previewApplyParams{}}, `{"dry_run":false}`, "live", "does not read dry_run"},
		{"case variant of a mode key", PreviewSpec{Params: previewApplyParams{}}, `{"APPLY":true}`, "preview", "only in case"},
		{"case variant of dry_run", PreviewSpec{Params: previewSnakeOnly{}}, `{"DRY_RUN":false}`, "preview", "only in case"},
		{"case variant of a LiveOneOf switch", oneOf, `{"Rollback":true}`, "preview", "only in case"},
		{"LiveOneOf live with none", oneOf, `{}`, "live", "exactly one"},
		{"LiveOneOf live with two", oneOf, `{"apply":true,"repair":true}`, "live", "exactly one"},
		{"LiveOneOf preview with one true", oneOf, `{"rollback":true}`, "preview", "contradicts rollback"},
		{"StartCheck", PreviewSpec{Params: previewApplyParams{}, StartCheck: func(_ map[string]json.RawMessage, live bool) error {
			if live {
				return errors.New("nope")
			}
			return nil
		}}, `{}`, "live", "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.spec.Resolve(json.RawMessage(tc.params), tc.mode)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			var me *ModeError
			if !errors.As(err, &me) {
				t.Fatalf("error %T is not a *ModeError", err)
			}
		})
	}
}

// The StartCheck sees the params Resolve is about to return, with the mode
// keys already written.
func TestPreviewSpec_StartCheckSeesResolvedParams(t *testing.T) {
	var seen map[string]json.RawMessage
	var seenLive bool
	spec := PreviewSpec{Params: previewTwoFamilies{}, StartCheck: func(m map[string]json.RawMessage, live bool) error {
		seen, seenLive = m, live
		return nil
	}}
	if _, err := spec.Resolve(json.RawMessage(`{}`), "live"); err != nil {
		t.Fatal(err)
	}
	if !seenLive || string(seen["apply"]) != "true" || string(seen["dry_run"]) != "false" {
		t.Fatalf("StartCheck saw live=%v %v", seenLive, seen)
	}
}

func TestCheckStatedModeAndSpelling(t *testing.T) {
	cases := []struct {
		params, mode, wantErr string
	}{
		{`{}`, "preview", ""},
		{`{"dry_run":true}`, "preview", ""},
		{`{"dry_run":true}`, "", ""},
		{`{"dry_run":false}`, "", ""},
		{`{"dry_run":false}`, "preview", "contradicts dry_run"},
		{`{"apply":true}`, "preview", "contradicts apply"},
		{`{"live":false}`, "live", "contradicts live"},
		{`{"dryRun":true}`, "live", "contradicts dryRun"},
		{`{"apply":true,"dry_run":true}`, "", "disagree"},
		{`{"live":"tomorrow"}`, "preview", ""}, // not a bool: left to the op
	}
	for _, tc := range cases {
		err := CheckStatedMode(json.RawMessage(tc.params), tc.mode)
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("CheckStatedMode(%s, %q) = %v, want %q", tc.params, tc.mode, err, tc.wantErr)
		}
	}
	for _, bad := range []string{`{"APPLY":true}`, `{"Dry_Run":true}`, `{"DRYRUN":false}`, `{"Live":true}`} {
		if err := CheckModeKeySpelling(json.RawMessage(bad)); err == nil || !strings.Contains(err.Error(), "only in case") {
			t.Errorf("CheckModeKeySpelling(%s) = %v, want a case refusal", bad, err)
		}
	}
	if err := CheckModeKeySpelling(json.RawMessage(`{"apply":true,"dry_run":false,"dryRun":false,"live":true,"limit":1}`)); err != nil {
		t.Errorf("exact spellings refused: %v", err)
	}
}

// encoding/json matches field names case-insensitively, which is why Resolve
// refuses case variants: without that, {"APPLY":true} reaches the Apply field
// of an op whose mode was resolved as a preview.
func TestPreviewSpec_CaseVariantWouldReachTheField(t *testing.T) {
	var p previewApplyParams
	if err := json.Unmarshal([]byte(`{"APPLY":true}`), &p); err != nil || !p.Apply {
		t.Fatalf("encoding/json no longer folds case (got %+v, %v); the refusal may be relaxed", p, err)
	}
}

func previewTestDef() OperationDef {
	return OperationDef{
		ID:           "test.preview-validate",
		Run:          func(context.Context, json.RawMessage, Reporter) error { return nil },
		ResumePolicy: ResumeDrop,
		Liveness:     LivenessManual,
	}
}

func TestValidateOpDef_Preview(t *testing.T) {
	ok := previewTestDef()
	ok.Preview = PreviewSpec{Params: previewApplyParams{}}
	if err := ValidateOpDef(ok); err != nil {
		t.Fatalf("valid preview refused: %v", err)
	}
	cases := map[string]func(*OperationDef){
		"declaration does not check": func(d *OperationDef) { d.Preview = PreviewSpec{Params: previewNoModeKey{}} },
		// The batch flush runs with {"subjects":[...]}, not the request's
		// params, so the resolved mode would be dropped.
		"batchable": func(d *OperationDef) { d.Preview = PreviewSpec{Params: previewApplyParams{}}; d.Batchable = true },
		// A preview merged into a queued live run (or the reverse) runs in
		// the other mode.
		"merge queued params": func(d *OperationDef) {
			d.Preview = PreviewSpec{Params: previewApplyParams{}}
			d.MergeQueuedParams = func(a, _ json.RawMessage) (json.RawMessage, bool, error) { return a, true, nil }
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := previewTestDef()
			mutate(&d)
			err := ValidateOpDef(d)
			if err == nil {
				t.Fatalf("ValidateOpDef accepted %s", name)
			}
			if !strings.Contains(err.Error(), "Preview") {
				t.Fatalf("error does not name Preview: %v", err)
			}
		})
	}
	plain := previewTestDef()
	plain.Batchable = true
	if err := ValidateOpDef(plain); err != nil {
		t.Fatalf("a batchable def without a preview is refused: %v", err)
	}
}
