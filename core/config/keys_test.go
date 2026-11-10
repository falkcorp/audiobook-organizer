// file: core/config/keys_test.go
// version: 1.0.0
// guid: f224e583-1d6b-4802-b4f3-62ff97c8cc3e
// last-edited: 2026-11-10

package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
)

// oidcProvider is a list element whose issuer is a trust root: protecting
// the field and not its key would let a lower credential replace the whole
// list, issuer included.
type oidcProvider struct {
	Issuer   string `json:"issuer" protect:"owner"`
	ClientID string `json:"client_id"`
}

// vault encodes itself; a tag inside it is still a tag inside its key.
type vault struct {
	Combination string `json:"combination" sensitive:"true"`
}

func (v vault) MarshalJSON() ([]byte, error) {
	type plain vault
	return json.Marshal(plain(v))
}

func (v *vault) UnmarshalJSON(b []byte) error {
	type plain vault
	return json.Unmarshal(b, (*plain)(v))
}

// shift is a list element with a restart-only field.
type shift struct {
	Hours int `json:"hours" reload:"restart"`
}

type nestedInSlice struct {
	Providers []oidcProvider `json:"providers"`
}
type nestedInMap struct {
	Providers map[string]oidcProvider `json:"providers"`
}
type nestedInPointer struct {
	Lead *oidcProvider `json:"lead"`
}
type nestedInArray struct {
	Pair [2]oidcProvider `json:"pair"`
}
type nestedInSelfEncoding struct {
	Box vault `json:"box"`
}
type nestedReload struct {
	Shifts []shift `json:"shifts"`
}
type nestedDeep struct {
	Teams map[string][]*oidcProvider `json:"teams"`
}

type taggedSlice struct {
	Providers []oidcProvider `json:"providers" protect:"owner"`
}
type taggedSelfEncoding struct {
	Box vault `json:"box" sensitive:"true"`
}
type taggedReload struct {
	Shifts []shift `json:"shifts" reload:"restart"`
}

// TestNestedTag_StricterThanKeyRefused: config protects, reloads and redacts
// whole keys, so a tag inside a key's value (a list, map, array or pointer
// element, or a struct that encodes itself) stricter than the key is a
// declaration error, never silently ignored. Tagging the key itself
// registers.
func TestNestedTag_StricterThanKeyRefused(t *testing.T) {
	refused := []struct {
		name  string
		sec   any
		field string
	}{
		{"slice of structs", config.Define("core.gotham.auth", nestedInSlice{}), "Issuer"},
		{"map of structs", config.Define("core.gotham.auth", nestedInMap{}), "Issuer"},
		{"pointer to struct", config.Define("core.gotham.auth", nestedInPointer{}), "Issuer"},
		{"array of structs", config.Define("core.gotham.auth", nestedInArray{}), "Issuer"},
		{"self-encoding struct", config.Define("core.gotham.auth", nestedInSelfEncoding{}), "Combination"},
		{"reload inside a list", config.Define("core.gotham.auth", nestedReload{}), "Hours"},
		{"map of slices of pointers", config.Define("core.gotham.auth", nestedDeep{}), "Issuer"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			st := bareStore(t)
			err := registerAny(st, c.sec)
			if !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), c.field) {
				t.Fatalf("Register = %v, want ErrInvalid naming field %s", err, c.field)
			}
			if err := st.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			for p := range st.Keys() {
				t.Errorf("the refused section left key %s", p)
			}
		})
	}
	accepted := []struct {
		name string
		sec  any
		key  config.Path
		want func(config.KeyState) bool
	}{
		{"protect on the list key", config.Define("core.gotham.auth", taggedSlice{}), "core.gotham.auth.providers",
			func(ks config.KeyState) bool { return ks.Protection == config.ProtectOwner }},
		{"sensitive on the self-encoding key", config.Define("core.gotham.auth", taggedSelfEncoding{}), "core.gotham.auth.box",
			func(config.KeyState) bool { return true }},
		{"reload on the list key", config.Define("core.gotham.auth", taggedReload{}), "core.gotham.auth.shifts",
			func(ks config.KeyState) bool { return ks.Reload == config.ReloadRestart }},
	}
	for _, c := range accepted {
		t.Run(c.name, func(t *testing.T) {
			st := bareStore(t)
			if err := registerAny(st, c.sec); err != nil {
				t.Fatalf("Register: %v", err)
			}
			if err := st.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			ks, ok := st.Lookup(c.key)
			if !ok || !c.want(ks) {
				t.Errorf("Lookup(%s) = %+v, %v", c.key, ks, ok)
			}
		})
	}
}

func bareStore(t *testing.T) *config.Store {
	t.Helper()
	st, err := config.NewStore(config.Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return st
}
