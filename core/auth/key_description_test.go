// file: core/auth/key_description_test.go
// version: 0.1.0
// guid: 78c2c144-408f-4d14-9c15-02af9348ff85
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// TestCheckKeyDescription: the one rule for a key's description, on its
// edges. Nothing is trimmed or shortened to fit.
func TestCheckKeyDescription(t *testing.T) {
	for name, tc := range map[string]struct {
		in string
		ok bool
	}{
		"empty":                 {"", true},
		"plain":                 {"Bat-Signal spare", true},
		"punctuation":           {"Gotham's \"Bat-Signal\" (spare) - 2026", true},
		"non-ascii":             {"café — 幽靈", true},
		"leading space":         {"  spare", true},
		"exactly the cap":       {strings.Repeat("a", auth.MaxKeyDescriptionRunes), true},
		"cap in multibyte":      {strings.Repeat("é", auth.MaxKeyDescriptionRunes), true},
		"one over":              {strings.Repeat("a", auth.MaxKeyDescriptionRunes+1), false},
		"one over in multibyte": {strings.Repeat("é", auth.MaxKeyDescriptionRunes+1), false},
		"line feed":             {"a\nb", false},
		"carriage return":       {"a\rb", false},
		"crlf":                  {"a\r\nb", false},
		"tab":                   {"a\tb", false},
		"nul":                   {"a\x00b", false},
		"escape":                {"a\x1bb", false},
		"delete":                {"a\x7fb", false},
		"c1 control":            {"a\u0085b", false},
		"invalid utf-8":         {"a\xffb", false},
	} {
		err := auth.CheckKeyDescription(tc.in)
		if tc.ok && err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: accepted", name)
		}
		if err != nil && strings.Contains(err.Error(), "a\xffb") {
			t.Errorf("%s: the error carries the text", name)
		}
	}
}

// TestAPIKey_DescriptionIsStoredAsGiven: CreateAPIKey stores the description
// untouched; a refused one is ErrBadInput and mints nothing.
func TestAPIKey_DescriptionIsStoredAsGiven(t *testing.T) {
	f := newFx(t)
	dick := f.user("dick", "gotham.detective")
	spec := auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}, Description: "  Used by the Batcomputer phone app  "}
	_, k, err := f.svc.CreateAPIKey(context.Background(), f.session(dick), spec)
	if err != nil || k.Description != spec.Description {
		t.Fatalf("create: %+v %v", k, err)
	}
	if stored := f.keysOf(dick.ID); len(stored) != 1 || stored[0].Description != spec.Description {
		t.Fatalf("stored: %+v", stored)
	}
	for name, d := range map[string]string{
		"too long":   strings.Repeat("a", auth.MaxKeyDescriptionRunes+1),
		"line break": "a\nb",
	} {
		spec.Description = d
		_, _, err := f.svc.CreateAPIKey(context.Background(), f.session(dick), spec)
		if !errors.Is(err, auth.ErrBadInput) {
			t.Errorf("%s: %v, want ErrBadInput", name, err)
		}
	}
	if n := f.keyCount(); n != 1 {
		t.Errorf("%d keys after two refused creates, want the 1 made first", n)
	}
}

// TestCheckImportAPIKey_Description: the import rule refuses a description
// CheckKeyDescription refuses, wraps ErrImportRejected, names the key by ID
// and never carries the text; the cap itself imports.
func TestCheckImportAPIKey_Description(t *testing.T) {
	hash := auth.HashCredential("abk_batmobile-spare-0001")
	ok := func(d string) *auth.APIKey {
		return &auth.APIKey{ID: "01JKEYDESC", Owner: "01JBRUCE", Scopes: []rbac.Permission{"gotham:patrol"}, Status: auth.KeyActive,
			ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Description: d}
	}
	if err := auth.CheckImportAPIKey(ok(strings.Repeat("a", auth.MaxKeyDescriptionRunes)), hash); err != nil {
		t.Fatalf("the cap itself: %v", err)
	}
	for name, d := range map[string]string{
		"too long": strings.Repeat("SECRET-TEXT ", 100),
		"newline":  "SECRET-TEXT\nsecond line",
	} {
		err := auth.CheckImportAPIKey(ok(d), hash)
		if !errors.Is(err, auth.ErrImportRejected) {
			t.Errorf("%s: %v, want ErrImportRejected", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "01JKEYDESC") || strings.Contains(err.Error(), "SECRET-TEXT") {
			t.Errorf("%s: error %q must name the key and not the text", name, err)
		}
	}
}
