// file: core/internal/authhttp/keys_description_test.go
// version: 0.1.0
// guid: e064f51d-81d3-4ef2-b40c-d16b9539c981
// last-edited: 2026-11-22

package authhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// descriptionOf is a key body naming a description, for the validation cases.
func descriptionOf(d string) KeyCreate {
	return KeyCreate{Name: "grapnel", Scopes: []string{"core:self"}, Description: d}
}

// TestKeyDescription_CreateShowsAndEveryListCarriesIt: a description sent on
// create is answered on the create, on the owner's list, on the all-keys list
// and on the single read, exactly as sent; a key made without one answers no
// description member at all.
func TestKeyDescription_CreateShowsAndEveryListCarriesIt(t *testing.T) {
	f := newKeysFx(t)
	const text = "Used by the Batcomputer phone app; reissue before the gala."
	p := f.e.session(f.bruce)
	created, err := call(specMeKeyCreate(), f.a.createMyKey, p, createKeyReq{Body: descriptionOf(text)})
	if err != nil || created.Body.APIKey.Description != text {
		t.Fatalf("create: %+v %v", created.Body.APIKey, err)
	}
	id := created.Body.APIKey.ID

	mine, err := call(specMeKeys(), f.a.myKeys, p, listReq{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	all, err := call(specKeysAll(), f.a.allKeys, p, listReq{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for name, page := range map[string][]APIKey{"my keys": mine.Items, "all keys": all.Items} {
		var found bool
		for _, k := range page {
			switch {
			case k.ID == id:
				found = true
				if k.Description != text {
					t.Errorf("%s: description %q, want %q", name, k.Description, text)
				}
			case k.Description != "":
				t.Errorf("%s: key %s without a description shows %q", name, k.ID, k.Description)
			}
		}
		if !found {
			t.Errorf("%s: the new key is not listed", name)
		}
	}
	got, err := call(specKeyGet(), f.a.getKey, p, keyReq{ID: id})
	if err != nil || got.Description != text {
		t.Fatalf("get: %+v %v", got, err)
	}
	// Stored on the record, so the next boot reads it back.
	if k := f.e.key(id); k.Description != text {
		t.Errorf("stored description %q, want %q", k.Description, text)
	}

	// A key with no description answers none (omitted, not "").
	plain, err := call(specMeKeyCreate(), f.a.createMyKey, p, createKeyReq{Body: descriptionOf("")})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plain.Body.APIKey)
	if strings.Contains(string(raw), "description") {
		t.Errorf("a key without a description answers %s", raw)
	}
}

// TestKeyDescription_ValidateRefusesWhatCoreRefuses: over the cap, a line
// break or any control character, and invalid UTF-8 are a 422 naming
// /body/description on both create routes and again on keySpec; exactly the
// cap, and non-ASCII text, pass. Nothing is stored for a refused body.
func TestKeyDescription_ValidateRefusesWhatCoreRefuses(t *testing.T) {
	cap := auth.MaxKeyDescriptionRunes
	bad := map[string]string{
		"one over the cap": strings.Repeat("a", cap+1),
		"line feed":        "scout\nGotham",
		"carriage return":  "scout\rGotham",
		"tab":              "scout\tGotham",
		"nul":              "scout\x00Gotham",
		"invalid utf-8":    "scout\xffGotham",
		"multibyte over":   strings.Repeat("é", cap+1),
	}
	for name, d := range bad {
		k := descriptionOf(d)
		for route, req := range map[string]interface{ Validate() error }{
			"me":   &createKeyReq{Body: k},
			"user": &userKeyCreateReq{ID: "u-dick", Body: k},
		} {
			err := req.Validate()
			wantErr(t, err, http.StatusUnprocessableEntity, "validation_failed")
			var he *httpapi.Error
			if !errors.As(err, &he) || len(he.Fields) != 1 || he.Fields[0].Field != "/body/description" {
				t.Errorf("%s on %s: fields %+v, want one at /body/description", name, route, he)
			}
		}
		_, err := k.keySpec("u-bruce")
		wantErr(t, err, http.StatusUnprocessableEntity, "validation_failed")
	}
	for name, d := range map[string]string{
		"exactly the cap":      strings.Repeat("a", cap),
		"multibyte at the cap": strings.Repeat("é", cap),
		"empty":                "",
		"punctuation":          "Gotham's \"Bat-Signal\" key (spare) - 2026",
	} {
		k := descriptionOf(d)
		if err := (&createKeyReq{Body: k}).Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if ks, err := k.keySpec("u-bruce"); err != nil || ks.Description != d {
			t.Errorf("%s: keySpec %+v %v", name, ks, err)
		}
	}

	// Through the route: the Service stores nothing for a refused body.
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	before := len(e.keysOfOwner(bruce.ID))
	_, err := call(specMeKeyCreate(), e.api().createMyKey, e.session(bruce), createKeyReq{Body: descriptionOf(strings.Repeat("a", cap+1))})
	wantErr(t, err, http.StatusUnprocessableEntity, "validation_failed")
	if n := len(e.keysOfOwner(bruce.ID)); n != before {
		t.Errorf("a refused description stored a key: %d keys, was %d", n, before)
	}
}
