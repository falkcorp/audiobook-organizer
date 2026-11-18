// file: internal/books/cutover/m4/v2_internal_test.go
// version: 1.0.0
// guid: 91d6c3a7-5e08-4b4f-a2d9-17c8e0f35b64
// last-edited: 2026-11-18

package m4

import (
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

func TestAttrFields(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{"", nil},
		{"{}", nil},
		{"null", nil},
		{`{"phase":"xml","inmates":12,"nested":{"a":1},"ok":true}`, map[string]string{"phase": "xml", "inmates": "12", "nested": `{"a":1}`, "ok": "true"}},
		{"not an object", map[string]string{"attrs": "not an object"}},
		{`["a"]`, map[string]string{"attrs": `["a"]`}},
	}
	for _, c := range cases {
		if got := attrFields(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("attrFields(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestPagesV2_Lines: the v2 lines' fields and sequences without a store. An
// error line's own attrs win over the plugin and def ID columns, which fill only
// what the attrs left unsaid.
func TestPagesV2_Lines(t *testing.T) {
	rr := &runRows{
		id: "01HBAT00000000000000000001",
		log2: []storage.KV{
			{Key: []byte("opv2:log:x:00000000000000000001:0000000004"), Value: []byte(`{"Level":"info","Message":"a","CreatedAt":"2026-11-05T03:00:00Z"}`)},
			{Key: []byte("opv2:log:x:00000000000000000002:garbage"), Value: []byte(`{"Level":"info","Message":"b","CreatedAt":"2026-11-05T03:00:01Z"}`)},
		},
		err2: []storage.KV{
			{Key: []byte("opv2:err:x:00000000000000000003"), Value: []byte(`{"Plugin":"maintenance","DefID":"d","Message":"c","Attrs":"{\"plugin\":\"mine\"}","OccurredAt":"2026-11-05T03:00:02Z"}`)},
		},
	}
	c := &converted{}
	if reason := c.pagesV2(rr); reason != "" {
		t.Fatal(reason)
	}
	if len(c.data.Pages) != 3 {
		t.Fatalf("%d pages", len(c.data.Pages))
	}
	// A key whose sequence does not parse is numbered by its place.
	if c.data.Pages[0].Seq != 4 || c.data.Pages[1].Seq != 2 || c.data.Pages[2].Seq != 5 {
		t.Errorf("sequences %d %d %d, want 4 2 5 (the error follows the highest line)", c.data.Pages[0].Seq, c.data.Pages[1].Seq, c.data.Pages[2].Seq)
	}
	f := c.data.Pages[2].Log.Fields
	if f["plugin"] != "mine" || f["def_id"] != "d" {
		t.Errorf("error fields = %v", f)
	}
	if c.data.Pages[2].Page != tasks.PageLog || c.data.Pages[2].Log.Level != "error" {
		t.Errorf("error page = %+v", c.data.Pages[2])
	}
}

func TestPagesV2_UndecodableLineRefused(t *testing.T) {
	for name, rr := range map[string]*runRows{
		"log":   {log2: []storage.KV{{Key: []byte("opv2:log:x:1:1"), Value: []byte("{")}}},
		"error": {err2: []storage.KV{{Key: []byte("opv2:err:x:1"), Value: []byte("{")}}},
	} {
		if got := (&converted{}).pagesV2(rr); got != reasonLogV2 {
			t.Errorf("%s: reason %q, want %q", name, got, reasonLogV2)
		}
	}
}
