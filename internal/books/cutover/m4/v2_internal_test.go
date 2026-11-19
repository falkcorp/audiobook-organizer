// file: internal/books/cutover/m4/v2_internal_test.go
// version: 1.1.0
// guid: 91d6c3a7-5e08-4b4f-a2d9-17c8e0f35b64
// last-edited: 2026-11-18

package m4

import (
	"encoding/base64"
	"reflect"
	"strconv"
	"strings"
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

// TestLogs_V2Lines: the v2 lines' fields and numbering without a store. The
// page is numbered 1..n in merged time order; an error line's own attrs win over
// the plugin and def ID columns, which fill only what the attrs left unsaid.
func TestLogs_V2Lines(t *testing.T) {
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
	if reason := c.logs(rr); reason != "" {
		t.Fatal(reason)
	}
	if len(c.data.Pages) != 3 {
		t.Fatalf("%d pages", len(c.data.Pages))
	}
	for i, p := range c.data.Pages {
		if p.Seq != uint64(i+1) {
			t.Errorf("page %d has sequence %d, want %d", i, p.Seq, i+1)
		}
	}
	if m := c.data.Pages[0].Log.Message + c.data.Pages[1].Log.Message + c.data.Pages[2].Log.Message; m != "abc" {
		t.Errorf("order %q, want abc", m)
	}
	f := c.data.Pages[2].Log.Fields
	if f["plugin"] != "mine" || f["def_id"] != "d" {
		t.Errorf("error fields = %v", f)
	}
	if c.data.Pages[2].Page != tasks.PageLog || c.data.Pages[2].Log.Level != "error" {
		t.Errorf("error page = %+v", c.data.Pages[2])
	}
}

func TestLogs_UndecodableLineRefused(t *testing.T) {
	for name, c := range map[string]struct {
		rr   *runRows
		want string
	}{
		"v3":    {&runRows{log: []storage.KV{{Key: []byte("opv3:log:x:1:1"), Value: []byte("{")}}}, reasonPage},
		"log":   {&runRows{log2: []storage.KV{{Key: []byte("opv2:log:x:1:1"), Value: []byte("{")}}}, reasonLogV2},
		"error": {&runRows{err2: []storage.KV{{Key: []byte("opv2:err:x:1"), Value: []byte("{")}}}, reasonLogV2},
		"v1":    {&runRows{log1: []storage.KV{{Key: []byte("operationlog:x:1:1"), Value: []byte("{")}}}, reasonV1Log},
	} {
		if got := (&converted{}).logs(c.rr); got != c.want {
			t.Errorf("%s: reason %q, want %q", name, got, c.want)
		}
	}
}

// TestLogs_RepeatedSequenceNumbersAcrossBoots: HEAD's log counter restarts at 1
// every process start, so a run that logged in two boots has two lines whose
// keys end in the same number (the key stays unique through its nanoseconds).
// Both lines must survive, in time order, with distinct page numbers.
func TestLogs_RepeatedSequenceNumbersAcrossBoots(t *testing.T) {
	line := func(key, msg, at string) storage.KV {
		return storage.KV{Key: []byte(key), Value: []byte(`{"at":"` + at + `","level":"info","message":"` + msg + `"}`)}
	}
	rr := &runRows{log: []storage.KV{
		line("opv3:log:x:00000000000000000100:0000000001", "boot one, first", "2026-11-05T03:00:00Z"),
		line("opv3:log:x:00000000000000000200:0000000002", "boot one, second", "2026-11-05T03:00:01Z"),
		line("opv3:log:x:00000000000000000300:0000000001", "boot two, first", "2026-11-05T04:00:00Z"),
		line("opv3:log:x:00000000000000000400:0000000002", "boot two, second", "2026-11-05T04:00:01Z"),
	}}
	c := &converted{}
	if reason := c.logs(rr); reason != "" {
		t.Fatal(reason)
	}
	want := []string{"boot one, first", "boot one, second", "boot two, first", "boot two, second"}
	if len(c.data.Pages) != len(want) {
		t.Fatalf("%d pages, want %d", len(c.data.Pages), len(want))
	}
	for i, p := range c.data.Pages {
		if p.Seq != uint64(i+1) || p.Log.Message != want[i] {
			t.Errorf("page %d = seq %d %q, want seq %d %q", i, p.Seq, p.Log.Message, i+1, want[i])
		}
	}
	if reason := c.unique(); reason != "" {
		t.Errorf("unique refused the numbered log: %s", reason)
	}
}

// TestLogs_FamilyTieBreak: lines of one instant order v3, v2 log, v2 error, v1.
func TestLogs_FamilyTieBreak(t *testing.T) {
	at := "2026-11-05T03:00:00Z"
	rr := &runRows{
		log:  []storage.KV{{Key: []byte("opv3:log:x:1:1"), Value: []byte(`{"at":"` + at + `","message":"v3"}`)}},
		log2: []storage.KV{{Key: []byte("opv2:log:x:1:1"), Value: []byte(`{"CreatedAt":"` + at + `","Message":"v2"}`)}},
		err2: []storage.KV{{Key: []byte("opv2:err:x:1"), Value: []byte(`{"OccurredAt":"` + at + `","Message":"err"}`)}},
		log1: []storage.KV{{Key: []byte("operationlog:x:1:1"), Value: []byte(`{"created_at":"` + at + `","message":"v1"}`)}},
	}
	c := &converted{}
	if reason := c.logs(rr); reason != "" {
		t.Fatal(reason)
	}
	var got string
	for _, p := range c.data.Pages {
		got += p.Log.Message + " "
	}
	if got != "v3 v2 err v1 " {
		t.Errorf("order %q, want v3 v2 err v1", got)
	}
}

// TestUnique_RefusesACollision: two pages with one key never reach the
// importer, whose plain put would let the second replace the first.
func TestUnique_RefusesACollision(t *testing.T) {
	for name, pages := range map[string][]tasks.PageEntry{
		"event":  {{Page: tasks.PageEvents, Seq: 1}, {Page: tasks.PageEvents, Seq: 1}},
		"intent": {{Page: tasks.PageIntents, Seq: 3}, {Page: tasks.PageIntents, Seq: 3}},
		"log":    {{Page: tasks.PageLog, Seq: 2}, {Page: tasks.PageLog, Seq: 2}},
		"item":   {{Page: tasks.PageItems, Key: "k"}, {Page: tasks.PageItems, Key: "k"}},
	} {
		c := &converted{}
		c.data.Pages = pages
		if got := c.unique(); got != reasonDuplicate {
			t.Errorf("%s: reason %q, want %q", name, got, reasonDuplicate)
		}
	}
	ok := &converted{}
	ok.data.Pages = []tasks.PageEntry{{Page: tasks.PageEvents, Seq: 1}, {Page: tasks.PageLog, Seq: 1}, {Page: tasks.PageItems, Key: "a"}, {Page: tasks.PageItems, Key: "b"}}
	if got := ok.unique(); got != "" {
		t.Errorf("distinct pages refused: %q", got)
	}
}

// TestItems_OutcomeRule: one item holding two outcomes imports as the outcome of
// the highest chunk (the greater outcome string on a tie), and the other rides
// whole, counted.
func TestItems_OutcomeRule(t *testing.T) {
	row := func(outcome string, chunk int) storage.KV {
		return storage.KV{Key: []byte("opv3:item:x:" + outcome + ":year-one"), Value: []byte(`{"key":"year-one","outcome":"` + outcome + `","error":"e","chunk":` + strconv.Itoa(chunk) + `}`)}
	}
	for name, c := range map[string]struct {
		rows          []storage.KV
		winner, loser string
	}{
		"later chunk wins":            {[]storage.KV{row("failed", 1), row("skipped:resumed", 2)}, "skipped:resumed", "failed"},
		"later chunk wins, order":     {[]storage.KV{row("skipped:resumed", 1), row("failed", 2)}, "failed", "skipped:resumed"},
		"tie: skipped follows failed": {[]storage.KV{row("failed", 3), row("skipped:resumed", 3)}, "skipped:resumed", "failed"},
	} {
		cv := &converted{}
		if reason := cv.items(&runRows{item: c.rows}); reason != "" {
			t.Fatalf("%s: %s", name, reason)
		}
		if len(cv.data.Pages) != 1 || cv.data.Pages[0].Item.Outcome != c.winner {
			t.Errorf("%s: pages %+v, want one %q", name, cv.data.Pages, c.winner)
		}
		raw, ok := cv.data.Checkpoints[CheckpointItemSupersededPrefix+c.loser+":year-one"]
		if !ok || !strings.Contains(string(raw), `"outcome":"`+c.loser+`"`) {
			t.Errorf("%s: the %q row was not carried: %v", name, c.loser, cv.data.Checkpoints)
		}
		if cv.superseded != 1 {
			t.Errorf("%s: superseded = %d, want 1", name, cv.superseded)
		}
	}
}

// TestCheckpoints_LosersAreCarried: the v1 checkpoint is chosen by precedence (an
// open run's opstate, then the checkpoint row's LegacyState, then opv2:state),
// and a candidate that loses is carried whole under its own name rather than
// discarded.
func TestCheckpoints_LosersAreCarried(t *testing.T) {
	ckpt := &storage.KV{Value: []byte(`{"v":1,"legacy_state":"` + base64.StdEncoding.EncodeToString([]byte("LEGACY")) + `"}`)}
	state2 := &storage.KV{Value: []byte("V2STATE")}
	state1 := &storage.KV{Value: []byte("OPSTATE")}
	for name, c := range map[string]struct {
		rr   *runRows
		open bool
		want map[string]string
	}{
		"open run: opstate wins": {&runRows{ckpt: ckpt, state2: state2, state1: state1}, true,
			map[string]string{CheckpointV1: "OPSTATE", CheckpointLegacyState: "LEGACY", CheckpointV2State: "V2STATE"}},
		"finished run: legacy state wins, opstate is deleted with the run": {&runRows{ckpt: ckpt, state2: state2, state1: state1}, false,
			map[string]string{CheckpointV1: "LEGACY", CheckpointV2State: "V2STATE"}},
		"no checkpoint row: opv2:state is the v1 checkpoint": {&runRows{state2: state2}, false,
			map[string]string{CheckpointV1: "V2STATE"}},
		"open run, opstate only": {&runRows{state1: state1}, true,
			map[string]string{CheckpointV1: "OPSTATE"}},
	} {
		cv := &converted{}
		if reason := cv.checkpoints(c.rr, c.open); reason != "" {
			t.Fatalf("%s: %s", name, reason)
		}
		got := map[string]string{}
		for k, v := range cv.data.Checkpoints {
			got[k] = string(v)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: checkpoints %v, want %v", name, got, c.want)
		}
	}
}
