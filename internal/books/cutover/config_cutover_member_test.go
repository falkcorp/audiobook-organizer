// file: internal/books/cutover/config_cutover_member_test.go
// version: 1.0.0
// guid: 8c1e6a40-3d97-4b52-a6f8-0b2d9e7c4a13
// last-edited: 2026-11-20

package cutover

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
)

// The maintenance window's three rows are members of core.tasks.windows, which
// core registers as ONE map-valued key. These tests pin what M2 does with them
// (the differential oracle found it staging three paths no key owns, so every
// stored window was quarantined and the flipped Config read a zero window).

func maintenanceBlob(members map[string]any) func(*rig) {
	return func(r *rig) {
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(t, lr)
			}
			rewriteBlob(t, lr, func(tree map[string]any) {
				delete(tree, "maintenance")
				if len(members) > 0 {
					m := map[string]any{}
					for k, v := range members {
						m[k] = v
					}
					tree["maintenance"] = m
				}
			})
		}
	}
}

func windowsStaged(snap *snapshot) (string, bool) {
	v, ok := snap.Config["core.tasks.windows"]
	return string(v), ok
}

func memberPaths(snap *snapshot) []string {
	var out []string
	for _, c := range snap.Calls {
		if strings.HasPrefix(c.Path, "core.tasks.windows.") {
			out = append(out, c.Path)
		}
	}
	return out
}

func TestConfigCutover_WindowMembersStageAsOneMapKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members map[string]any
		want    string // the staged core.tasks.windows value; "" when nothing is staged
	}{
		{"all three non-default", map[string]any{"enabled": false, "window_start": 3, "window_end": 6},
			`{"nightly":{"enabled":false,"end":6,"start":3}}`},
		{"one stored member keeps its siblings at HEAD's default, never zero", map[string]any{"window_start": 3},
			`{"nightly":{"enabled":true,"end":4,"start":3}}`},
		{"the stored default stages nothing", map[string]any{"enabled": true, "window_start": 1, "window_end": 4}, ""},
		{"no maintenance block stages nothing", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := newRig(t, "stage1", seedKnown, maintenanceBlob(tc.members)).run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			if got := memberPaths(snap); len(got) != 0 {
				t.Errorf("M2 staged member paths core does not register: %v", got)
			}
			got, ok := windowsStaged(snap)
			if tc.want == "" {
				if ok {
					t.Errorf("core.tasks.windows staged %s, want nothing (the default)", got)
				}
			} else if !ok || got != tc.want {
				t.Errorf("core.tasks.windows = %s (staged %v), want %s", got, ok, tc.want)
			}
			for _, q := range snap.Quarantine {
				if strings.Contains(q.Key, "maintenance.") {
					t.Errorf("a maintenance member was quarantined: %+v", q)
				}
			}
		})
	}
}

func TestConfigCutover_WindowRefusalQuarantinesEveryMember(t *testing.T) {
	r := newRig(t, "stage1", seedKnown, maintenanceBlob(map[string]any{"enabled": false, "window_start": 99, "window_end": 6}))
	r.refuse = map[string]error{"core.tasks.windows": &config.FieldError{Path: "core.tasks.windows", Err: fmt.Errorf("%w: start out of range", config.ErrInvalid)}}
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M2 failed on a refused value: %v", err)
	}
	if _, ok := windowsStaged(snap); ok {
		t.Error("core.tasks.windows was stored although the registry refused it")
	}
	got := map[string]json.RawMessage{}
	for _, q := range snap.Quarantine {
		if strings.HasPrefix(q.Key, "setting:config_blob#maintenance.") {
			got[strings.TrimPrefix(q.Key, "setting:config_blob#")] = json.RawMessage(q.Value)
			if q.Reason != reasonRefusedValue {
				t.Errorf("%s quarantined as %q, want %q", q.Key, q.Reason, reasonRefusedValue)
			}
		}
	}
	for head, want := range map[string]string{"maintenance.enabled": "false", "maintenance.window_start": "99", "maintenance.window_end": "6"} {
		if string(got[head]) != want {
			t.Errorf("%s quarantined with %q, want the stored value %s (the refusal must keep every member, not only the first)", head, got[head], want)
		}
	}
}

func TestMemberOf_WithMember(t *testing.T) {
	whole := json.RawMessage(`{"nightly":{"enabled":true,"end":4,"start":1},"weekly":{"start":2}}`)
	if v, ok, err := MemberOf(whole, "nightly.start"); err != nil || !ok || string(v) != "1" {
		t.Fatalf("MemberOf nightly.start = %s, %v, %v", v, ok, err)
	}
	for _, rel := range []string{"nightly.nope", "monthly.start", "nightly.start.deeper"} {
		if _, ok, err := MemberOf(whole, rel); ok || err != nil {
			t.Errorf("MemberOf %s = found %v, err %v, want absent", rel, ok, err)
		}
	}
	got, err := WithMember(whole, "nightly.start", json.RawMessage(`3`))
	if err != nil || string(got) != `{"nightly":{"enabled":true,"end":4,"start":3},"weekly":{"start":2}}` {
		t.Fatalf("WithMember kept the siblings? %s, %v", got, err)
	}
	for _, empty := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(``)} {
		got, err := WithMember(empty, "nightly.start", json.RawMessage(`3`))
		if err != nil || string(got) != `{"nightly":{"start":3}}` {
			t.Errorf("WithMember(%q) = %s, %v", empty, got, err)
		}
	}
	if _, err := WithMember(json.RawMessage(`[1]`), "a.b", json.RawMessage(`1`)); err == nil {
		t.Error("a whole that is not an object was overwritten")
	}
	for _, bad := range []string{"", ".a", "a.", "a..b"} {
		if _, err := WithMember(whole, bad, json.RawMessage(`1`)); err == nil {
			t.Errorf("WithMember accepted the path %q", bad)
		}
		if _, _, err := MemberOf(whole, bad); err == nil {
			t.Errorf("MemberOf accepted the path %q", bad)
		}
	}
}
