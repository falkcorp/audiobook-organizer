// file: internal/cfaccesspolicy/servicetoken_test.go
// version: 1.0.0
// guid: 5d9c3b71-2e84-4f06-a7b1-8c0e4d2f6a19
// last-edited: 2026-10-23

package cfaccesspolicy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	guestTok  = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	guestTok2 = "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	ownerTok  = "90a520fb-0000-4000-8000-000000000000"
)

// booksPolicy is a non-identity policy holding the owner's own token rule
// and, to prove it is never touched, an any_valid_service_token rule.
func booksPolicy() map[string]any {
	return map[string]any{
		"id": testPolicy, "name": "books", "decision": "non_identity",
		"include": []any{
			map[string]any{"service_token": map[string]any{"token_id": ownerTok}},
			map[string]any{"any_valid_service_token": map[string]any{}},
		},
	}
}

func TestEnsureServiceToken_AddsExactlyOneRule(t *testing.T) {
	f := &fakeCF{policy: booksPolicy()}
	c := newClient(t, f)
	res, err := c.EnsureServiceToken(context.Background(), guestTok)
	if err != nil || res != Added {
		t.Fatalf("res=%q err=%v", res, err)
	}
	inc := f.putBody["include"].([]any)
	if len(inc) != 3 {
		t.Fatalf("include has %d rules, want 3", len(inc))
	}
	last, _ := json.Marshal(inc[2])
	if string(last) != `{"service_token":{"token_id":"`+guestTok+`"}}` {
		t.Fatalf("appended rule = %s", last)
	}
	// The pre-existing rules are byte-for-byte what was read.
	for i, want := range []string{
		`{"service_token":{"token_id":"` + ownerTok + `"}}`,
		`{"any_valid_service_token":{}}`,
	} {
		got, _ := json.Marshal(inc[i])
		if string(got) != want {
			t.Fatalf("rule %d rewritten: %s", i, got)
		}
	}
}

func TestEnsureServiceToken_NeverAddsAnyValidServiceToken(t *testing.T) {
	// Starting from a policy that has no such rule, the code must not add one.
	f := &fakeCF{policy: map[string]any{
		"name": "books", "decision": "non_identity",
		"include": []any{map[string]any{"service_token": map[string]any{"token_id": guestTok2}}},
	}}
	c := newClient(t, f)
	if _, err := c.EnsureServiceToken(context.Background(), guestTok); err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(f.putBody); strings.Contains(string(b), "any_valid_service_token") {
		t.Fatalf("PUT body carries any_valid_service_token: %s", b)
	}
}

func TestEnsureServiceToken_Idempotent(t *testing.T) {
	f := &fakeCF{policy: booksPolicy()}
	c := newClient(t, f)
	if _, err := c.EnsureServiceToken(context.Background(), guestTok); err != nil {
		t.Fatal(err)
	}
	f.methods = nil
	res, err := c.EnsureServiceToken(context.Background(), strings.ToUpper(guestTok))
	if err != nil || res != AlreadyPresent {
		t.Fatalf("res=%q err=%v", res, err)
	}
	if strings.Join(f.methods, ",") != "GET" {
		t.Fatalf("an already-present token must not PUT: %v", f.methods)
	}
}

func TestRemoveServiceToken_DropsOnlyThatRule(t *testing.T) {
	f := &fakeCF{policy: booksPolicy()}
	c := newClient(t, f)
	for _, id := range []string{guestTok, guestTok2} {
		if _, err := c.EnsureServiceToken(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	res, err := c.RemoveServiceToken(context.Background(), guestTok)
	if err != nil || res != Removed {
		t.Fatalf("res=%q err=%v", res, err)
	}
	b, _ := json.Marshal(f.putBody["include"])
	got := string(b)
	for _, must := range []string{ownerTok, guestTok2, "any_valid_service_token"} {
		if !strings.Contains(got, must) {
			t.Fatalf("remove dropped %s: %s", must, got)
		}
	}
	if strings.Contains(got, guestTok+`"`) {
		t.Fatalf("rule still present: %s", got)
	}
	// Removing it again is a no-op.
	f.methods = nil
	res, err = c.RemoveServiceToken(context.Background(), guestTok)
	if err != nil || res != NotPresent || strings.Join(f.methods, ",") != "GET" {
		t.Fatalf("second remove: res=%q err=%v methods=%v", res, err, f.methods)
	}
}

func TestRemoveServiceToken_DoesNotMatchLookalikeRules(t *testing.T) {
	// A rule with extra keys, or a different kind naming the same id, is not ours.
	f := &fakeCF{policy: map[string]any{
		"name": "books", "decision": "non_identity",
		"include": []any{
			map[string]any{"service_token": map[string]any{"token_id": guestTok, "extra": "x"}},
			map[string]any{"email": map[string]any{"email": guestTok}},
			map[string]any{"service_token": map[string]any{"token_id": guestTok2}},
		},
	}}
	c := newClient(t, f)
	res, err := c.RemoveServiceToken(context.Background(), guestTok)
	if err != nil || res != NotPresent {
		t.Fatalf("res=%q err=%v", res, err)
	}
	if len(f.putBody) != 0 {
		t.Fatalf("a PUT was sent: %v", f.putBody)
	}
}

func TestServiceToken_ProtectedAndMalformedIDsNeverReachTheNetwork(t *testing.T) {
	f := &fakeCF{policy: booksPolicy()}
	c := newClient(t, f)
	for _, prefix := range ProtectedTokenPrefixes() {
		id := prefix + "-0000-4000-8000-000000000000"
		if _, err := c.RemoveServiceToken(context.Background(), id); !errors.Is(err, ErrProtectedToken) {
			t.Errorf("Remove(%s) = %v", id, err)
		}
		if _, err := c.EnsureServiceToken(context.Background(), id); !errors.Is(err, ErrProtectedToken) {
			t.Errorf("Ensure(%s) = %v", id, err)
		}
	}
	for _, id := range []string{"", "any_valid_service_token", "not-a-uuid", guestTok + "x"} {
		if _, err := c.EnsureServiceToken(context.Background(), id); err == nil {
			t.Errorf("Ensure(%q) accepted", id)
		}
	}
	if len(f.methods) != 0 {
		t.Fatalf("requests were sent: %v", f.methods)
	}
}

func TestServiceToken_EmailRulesUntouched(t *testing.T) {
	f := &fakeCF{policy: newPolicy()}
	c := newClient(t, f)
	if _, err := c.EnsureServiceToken(context.Background(), guestTok); err != nil {
		t.Fatal(err)
	}
	inc := f.putBody["include"].([]any)
	if !hasEmail(inc, "existing@example.com") || len(inc) != 3 {
		t.Fatalf("include = %v", inc)
	}
}

func TestAPIError_IsForbiddenFollowsStatusNotText(t *testing.T) {
	f := &fakeCF{policy: booksPolicy(), failPut: true} // PUT answers 403
	c := newClient(t, f)
	_, err := c.EnsureServiceToken(context.Background(), guestTok)
	if !IsForbidden(err) {
		t.Fatalf("a 403 on save was not recognised: %v", err)
	}
	if IsForbidden(errors.New("HTTP 403: spoofed text in a plain error")) {
		t.Fatal("IsForbidden matched on text")
	}
}
