// file: core/storage/codec/codec_test.go
// version: 1.0.0
// guid: 87b982b4-27e1-482a-bc84-b348e2963527
// last-edited: 2026-11-07

package codec

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type villain struct {
	Name   string `json:"name"`
	Arrest int    `json:"arrests"`
}

func TestJSONRoundTripAndEnvelope(t *testing.T) {
	c := JSON[villain](3)
	b, err := c.Encode(villain{Name: "Joker", Arrest: 12})
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 'j' {
		t.Fatalf("tag = %q, want 'j'", b[0])
	}
	if want := `{"v":3,"name":"Joker","arrests":12}`; string(b[1:]) != want {
		t.Fatalf("body = %s, want %s", b[1:], want)
	}
	got, err := c.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != (villain{Name: "Joker", Arrest: 12}) {
		t.Fatalf("decoded %+v", got)
	}
}

func TestJSONEmptyObject(t *testing.T) {
	c := JSON[struct{}](1)
	b, err := c.Encode(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if string(b[1:]) != `{"v":1}` {
		t.Fatalf("body = %s", b[1:])
	}
	if _, err := c.Decode(b); err != nil {
		t.Fatal(err)
	}
}

func TestJSONRejectsOtherVersions(t *testing.T) {
	b, err := JSON[villain](2).Encode(villain{Name: "Bane"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := JSON[villain](1).Decode(b); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("decode of v2 by v1 = %v, want ErrUnknownVersion", err)
	}
	if _, err := JSON[villain](1).Decode([]byte(`j{"name":"Bane"}`)); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("decode without v = %v, want ErrUnknownVersion", err)
	}
	if _, err := JSON[villain](1).Decode([]byte(`{"v":1}`)); err == nil {
		t.Fatal("decode without the tag succeeded")
	}
}

func TestJSONRefusesNonObjectAndOwnV(t *testing.T) {
	if _, err := JSON[int](1).Encode(7); err == nil {
		t.Fatal("JSON of an int succeeded")
	}
	type clash struct {
		V int `json:"v"`
	}
	if _, err := JSON[clash](1).Encode(clash{V: 2}); err == nil || !strings.Contains(err.Error(), `"v"`) {
		t.Fatalf("JSON of a type with its own v = %v, want a refusal", err)
	}
	if _, err := JSON[*villain](1).Encode(nil); err == nil {
		t.Fatal("JSON of a nil pointer succeeded")
	}
}

func TestRawRoundTrip(t *testing.T) {
	in := []byte{0, 1, 2, 'r'}
	b, err := Raw().Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 'r' || !bytes.Equal(b[1:], in) {
		t.Fatalf("raw encoding %v", b)
	}
	got, err := Raw().Decode(b)
	if err != nil || !bytes.Equal(got, in) {
		t.Fatalf("raw decode = %v, %v", got, err)
	}
	if _, err := Raw().Decode([]byte("jnope")); err == nil {
		t.Fatal("raw decode of a JSON value succeeded")
	}
}

func TestZstdCompressesAboveMinSize(t *testing.T) {
	c := Zstd(Raw(), 64)
	small := []byte("robin")
	b, err := c.Encode(small)
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 'r' {
		t.Fatalf("small value tag = %q, want the inner 'r'", b[0])
	}
	big := bytes.Repeat([]byte("nightwing "), 200)
	zb, err := c.Encode(big)
	if err != nil {
		t.Fatal(err)
	}
	if zb[0] != 'z' || len(zb) >= len(big) {
		t.Fatalf("big value tag %q length %d (raw %d), want compressed 'z'", zb[0], len(zb), len(big))
	}
	for _, enc := range [][]byte{b, zb} {
		got, err := c.Decode(enc)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, small) && !bytes.Equal(got, big) {
			t.Fatalf("zstd round trip lost the value")
		}
	}
}

func TestLegacyJSONIsPlainEncodingJSON(t *testing.T) {
	c := LegacyJSON[villain]()
	b, err := c.Encode(villain{Name: "Riddler", Arrest: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"name":"Riddler","arrests":3}` {
		t.Fatalf("legacy encoding %s", b)
	}
	// encoding/json v1 matches member names case-insensitively, as HEAD's
	// rows rely on.
	got, err := c.Decode([]byte(`{"NAME":"Riddler","Arrests":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, villain{Name: "Riddler", Arrest: 3}) {
		t.Fatalf("legacy decode %+v", got)
	}
}
