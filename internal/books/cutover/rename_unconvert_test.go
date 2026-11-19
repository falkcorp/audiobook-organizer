// file: internal/books/cutover/rename_unconvert_test.go
// version: 1.0.0
// guid: 6b0f2c1e-3a54-4f0e-9a37-2f5d1c7e8b41
// last-edited: 2026-11-19

package cutover

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/secrets"
)

// roundTrip is one HEAD value through the forward converter and back.
type roundTrip struct {
	name string // the case
	conv string // the converter
	head string // HEAD's effective value
	// back is what Unconvert returns after Convert; "" means head itself.
	back string
	// lossy names the information the round trip loses ("" for an exact one).
	lossy string
}

// roundTripCases covers every converter the rename table names. The
// completeness test iterates the table and fails for a converter with no case
// here, so a converter added to the forward table without an inverse (and a
// case) cannot pass.
var roundTripCases = []roundTrip{
	{name: "identity", conv: "", head: `"The Long Halloween"`},
	{name: "auth on", conv: ConvertAuthMode, head: `true`},
	{name: "auth off", conv: ConvertAuthMode, head: `false`},
	{name: "days", conv: ConvertDaysToDuration, head: `7`},
	{name: "days zero", conv: ConvertDaysToDuration, head: `0`},
	{name: "days 90", conv: ConvertDaysToDuration, head: `90`},
	{name: "mib", conv: ConvertMiBToBytes, head: `4`},
	{name: "mib zero", conv: ConvertMiBToBytes, head: `0`},
	{name: "log retention keeps", conv: ConvertLogRetention, head: `30`},
	{name: "log retention keep-forever", conv: ConvertLogRetention, head: `-1`},
	{name: "log retention other negative", conv: ConvertLogRetention, head: `-5`, back: `-1`,
		lossy: "log-retention negative: every negative value is HEAD's keep-forever, which returns as -1"},
	{name: "updater on", conv: ConvertUpdaterMode, head: `true`},
	{name: "updater off", conv: ConvertUpdaterMode, head: `false`},
	{name: "channel develop", conv: ConvertUpdaterChannel, head: `"develop"`},
	{name: "channel stable", conv: ConvertUpdaterChannel, head: `"stable"`},
	{name: "channel literal prerelease", conv: ConvertUpdaterChannel, head: `"prerelease"`, back: `"develop"`,
		lossy: "updater-channel: HEAD's literal prerelease (never a HEAD channel) returns as develop"},
	{name: "check 90 minutes", conv: ConvertUpdaterCheck, head: `90`},
	{name: "standard schedule", conv: ConvertScheduleStandard, head: `{"enabled":true,"interval":60}`},
	{name: "standard schedule disabled keeps its interval", conv: ConvertScheduleStandard, head: `{"enabled":false,"interval":15}`},
	{name: "standard schedule zero interval", conv: ConvertScheduleStandard, head: `{"enabled":true,"interval":0}`},
	{name: "standard schedule on_startup", conv: ConvertScheduleStandard, head: `{"enabled":true,"interval":30,"on_startup":true}`,
		back:  `{"enabled":true,"interval":30}`,
		lossy: "schedule on_startup: the canonical value has no field for it, so the inverse leaves the key out"},
	{name: "interval-only on", conv: ConvertScheduleInterval, head: `{"interval":720}`},
	{name: "interval-only off", conv: ConvertScheduleInterval, head: `{"interval":0}`},
	{name: "interval-only ignores enabled", conv: ConvertScheduleInterval, head: `{"enabled":false,"interval":720,"on_startup":true}`,
		back:  `{"interval":720}`,
		lossy: "schedule-interval: HEAD never reads enabled or on_startup of an interval-only entry, so neither returns"},
	{name: "full scan", conv: ConvertScheduleFullScan, head: `{"enabled":true,"period_hours":168}`},
	{name: "full scan due-check interval", conv: ConvertScheduleFullScan, head: `{"enabled":true,"interval":5,"period_hours":24}`,
		back:  `{"enabled":true,"period_hours":24}`,
		lossy: "schedule-full-scan: the due-check interval has no canonical field, so the inverse leaves it out"},
	{name: "csv list", conv: ConvertCSVList, head: `"Gotham,Metropolis,Bludhaven"`},
	{name: "csv list spacing and empties", conv: ConvertCSVList, head: `"Gotham, Metropolis ,,Bludhaven"`, back: `"Gotham,Metropolis,Bludhaven"`,
		lossy: "csv empties: empty items are dropped and spacing is normalised to a bare comma"},
}

// TestUnconvert_RoundTripsThroughConvert: HEAD value -> Convert -> Unconvert
// gives the HEAD value back, except where a case names what is lost.
func TestUnconvert_RoundTripsThroughConvert(t *testing.T) {
	for _, c := range roundTripCases {
		t.Run(c.name, func(t *testing.T) {
			canon, _, err := Convert(c.conv, json.RawMessage(c.head))
			if err != nil {
				t.Fatalf("Convert(%q, %s): %v", c.conv, c.head, err)
			}
			back, err := Unconvert(c.conv, canon)
			if err != nil {
				t.Fatalf("Unconvert(%q, %s): %v", c.conv, canon, err)
			}
			want := c.back
			if want == "" {
				want = c.head
			}
			if !jsonEq(t, string(back), want) {
				t.Errorf("Unconvert(%q, %s) = %s, want %s", c.conv, canon, back, want)
			}
			// A key the case says is lost must really be absent, not null or zero:
			// jsonEq cannot tell a missing key from nothing, so check the keys.
			var got, wantObj map[string]any
			if json.Unmarshal(back, &got) == nil && json.Unmarshal([]byte(want), &wantObj) == nil {
				if len(got) != len(wantObj) {
					t.Errorf("Unconvert(%q) keys = %v, want %v", c.conv, unconvKeys(got), unconvKeys(wantObj))
				}
			}
		})
	}
}

func unconvKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestUnconvert_EveryTableConverterHasAnInverse iterates the generated rename
// table: a converter name a row uses must have a round-trip case above, or be
// one of the two that stage nothing and say so with ErrNotApplicable. A
// converter added to the forward table without an inverse fails here.
func TestUnconvert_EveryTableConverterHasAnInverse(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range roundTripCases {
		covered[c.conv] = true
	}
	used := map[string]bool{}
	for _, r := range Table().Rows {
		for _, tg := range r.Targets {
			if tg.Convert != "" {
				used[tg.Convert] = true
			}
		}
	}
	if len(used) == 0 {
		t.Fatal("the rename table names no converter: the iteration is blind")
	}
	for name := range used {
		if _, err := Unconvert(name, json.RawMessage(`null`)); errors.Is(err, errUnknownConverter) {
			t.Errorf("converter %q is in the rename table and Unconvert does not know it: write its inverse", name)
			continue
		}
		if covered[name] {
			continue
		}
		// No round-trip case: only a converter that stages nothing forward, for
		// any input, may answer ErrNotApplicable instead.
		if _, err := Unconvert(name, json.RawMessage(`null`)); !errors.Is(err, ErrNotApplicable) {
			t.Errorf("converter %q is in the rename table with no round-trip case: add its inverse and a case", name)
			continue
		}
		for _, in := range []string{`null`, `true`, `0`, `"x"`, `{}`} {
			if _, _, err := Convert(name, json.RawMessage(in)); !errors.Is(err, ErrNotStaged) {
				t.Errorf("converter %q answers ErrNotApplicable but Convert(%s) = %v: it stages something, so it needs an inverse", name, in, err)
			}
		}
	}
	// And every name the package defines is known to Unconvert.
	for _, name := range []string{ConvertNeverStored, ConvertAuthMode, ConvertDaysToDuration, ConvertMiBToBytes, ConvertLogRetention,
		ConvertOIDCFold, ConvertUpdaterMode, ConvertUpdaterChannel, ConvertUpdaterCheck, ConvertScheduleStandard,
		ConvertScheduleInterval, ConvertScheduleFullScan, ConvertCSVList} {
		if !knownConvert(name) {
			t.Fatalf("test list out of date: %q", name)
		}
		if _, err := Unconvert(name, json.RawMessage(`null`)); errors.Is(err, errUnknownConverter) {
			t.Errorf("Unconvert does not know %q", name)
		}
	}
}

func TestUnconvert_UnknownName(t *testing.T) {
	_, err := Unconvert("no-such-converter", json.RawMessage(`1`))
	for _, want := range []error{ErrNotApplicable, ErrConvertInput, errUnknownConverter} {
		if !errors.Is(err, want) {
			t.Errorf("unknown converter error %v does not wrap %v", err, want)
		}
	}
}

// TestUnconvert_NotApplicable: the two converters that stage nothing.
func TestUnconvert_NotApplicable(t *testing.T) {
	for _, conv := range []string{ConvertNeverStored, ConvertOIDCFold} {
		_, err := Unconvert(conv, json.RawMessage(`"/var/lib/batcave/gotham.db"`))
		if !errors.Is(err, ErrNotApplicable) || errors.Is(err, errUnknownConverter) {
			t.Errorf("Unconvert(%q) = %v, want ErrNotApplicable", conv, err)
		}
	}
}

// TestUnconvert_LossyCases pins the exact behaviour at each place the inverse
// cannot be the other half of Convert.
func TestUnconvert_LossyCases(t *testing.T) {
	ok := func(t *testing.T, conv, in, want string) {
		t.Helper()
		got, err := Unconvert(conv, json.RawMessage(in))
		if err != nil {
			t.Fatalf("Unconvert(%q, %s): %v", conv, in, err)
		}
		if !jsonEq(t, string(got), want) {
			t.Errorf("Unconvert(%q, %s) = %s, want %s", conv, in, got, want)
		}
	}
	refused := func(t *testing.T, conv, in string, want error) {
		t.Helper()
		got, err := Unconvert(conv, json.RawMessage(in))
		if !errors.Is(err, want) {
			t.Errorf("Unconvert(%q, %s) = %s, %v; want %v", conv, in, got, err, want)
		}
		if err != nil && strings.Contains(err.Error(), strings.Trim(in, `"`)) && len(strings.Trim(in, `"`)) > 2 {
			t.Errorf("the error carries the value: %v", err)
		}
	}

	t.Run("log-retention: canonical 0 is HEAD keep-forever, never HEAD 0", func(t *testing.T) {
		// HEAD reads a stored 0 as the 90-day default, so an identity inverse
		// would turn "keep forever" into "keep 90 days".
		ok(t, ConvertLogRetention, `0`, `-1`)
		ok(t, ConvertLogRetention, `90`, `90`)
		refused(t, ConvertLogRetention, `-3`, ErrNotRepresentable)
		refused(t, ConvertLogRetention, `1.5`, ErrNotRepresentable)
		refused(t, ConvertLogRetention, `"a"`, ErrConvertInput)
	})
	t.Run("updater-check <= 0: forward stages nothing, inverse of a disabled check is 0", func(t *testing.T) {
		for _, head := range []string{`0`, `-5`} {
			if _, _, err := Convert(ConvertUpdaterCheck, json.RawMessage(head)); !errors.Is(err, ErrNotStaged) {
				t.Fatalf("Convert(updater-check, %s) = %v, want ErrNotStaged", head, err)
			}
		}
		ok(t, ConvertUpdaterCheck, `{"enabled":false}`, `0`)
		ok(t, ConvertUpdaterCheck, `{"enabled":false,"every":"2h"}`, `0`)
		ok(t, ConvertUpdaterCheck, `{"enabled":true,"every":"90m"}`, `90`)
		refused(t, ConvertUpdaterCheck, `{"enabled":true}`, ErrNotRepresentable)
		refused(t, ConvertUpdaterCheck, `{"enabled":true,"every":"90s"}`, ErrNotRepresentable)
		refused(t, ConvertUpdaterCheck, `{"enabled":true,"every":"-1h"}`, ErrNotRepresentable)
		refused(t, ConvertUpdaterCheck, `{"enabled":true,"daily_at":"03:00"}`, ErrNotRepresentable)
		refused(t, ConvertUpdaterCheck, `{"enabled":true,"cron":"0 3 * * *"}`, ErrNotRepresentable)
		refused(t, ConvertUpdaterCheck, `{"enabled":true,"window":"night"}`, ErrNotRepresentable)
		refused(t, ConvertUpdaterCheck, `{"enabled":true,"every":"1h","order":2}`, ErrNotRepresentable)
		refused(t, ConvertUpdaterCheck, `{"enabled":true,"bogus":1}`, ErrConvertInput)
		refused(t, ConvertUpdaterCheck, `5`, ErrConvertInput)
	})
	t.Run("schedule on_startup: the inverse never writes it", func(t *testing.T) {
		got, err := Unconvert(ConvertScheduleStandard, json.RawMessage(`{"enabled":true,"every":"30m"}`))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatal(err)
		}
		if _, has := m["on_startup"]; has {
			t.Errorf("on_startup written: %s", got)
		}
		if m["interval"] != float64(30) || m["enabled"] != true || len(m) != 2 {
			t.Errorf("got %s", got)
		}
	})
	t.Run("schedule: what HEAD cannot hold is refused", func(t *testing.T) {
		for _, conv := range []string{ConvertScheduleStandard, ConvertScheduleInterval, ConvertScheduleFullScan} {
			refused(t, conv, `{"enabled":true,"daily_at":"03:00"}`, ErrNotRepresentable)
			refused(t, conv, `{"enabled":true,"cron":"0 3 * * *"}`, ErrNotRepresentable)
			refused(t, conv, `{"enabled":true,"window":"night"}`, ErrNotRepresentable)
			refused(t, conv, `{"enabled":true,"every":"1h","order":1}`, ErrNotRepresentable)
			refused(t, conv, `{"enabled":true,"every":"-1h"}`, ErrNotRepresentable)
			refused(t, conv, `{"enabled":true,"nope":1}`, ErrConvertInput)
			refused(t, conv, `true`, ErrConvertInput)
		}
		refused(t, ConvertScheduleStandard, `{"enabled":true,"every":"90s"}`, ErrNotRepresentable)
		refused(t, ConvertScheduleFullScan, `{"enabled":true,"every":"90m"}`, ErrNotRepresentable)
		ok(t, ConvertScheduleStandard, `{"enabled":true}`, `{"enabled":true,"interval":0}`)
		ok(t, ConvertScheduleFullScan, `{"enabled":true}`, `{"enabled":true,"period_hours":0}`)
		ok(t, ConvertScheduleFullScan, `{"enabled":false,"every":"48h"}`, `{"enabled":false,"period_hours":48}`)
	})
	t.Run("schedule-interval: disabled stays off, enabled needs an interval", func(t *testing.T) {
		// HEAD runs the task iff interval > 0, so a disabled canonical value
		// must come back 0 whatever its every says.
		ok(t, ConvertScheduleInterval, `{"enabled":false,"every":"6h"}`, `{"interval":0}`)
		ok(t, ConvertScheduleInterval, `{"enabled":false}`, `{"interval":0}`)
		ok(t, ConvertScheduleInterval, `{"enabled":true,"every":"6h"}`, `{"interval":360}`)
		refused(t, ConvertScheduleInterval, `{"enabled":true}`, ErrNotRepresentable)
		refused(t, ConvertScheduleInterval, `{"enabled":true,"every":"90s"}`, ErrNotRepresentable)
	})
	t.Run("csv empties: an empty list is an empty string", func(t *testing.T) {
		for _, head := range []string{`""`, `" , ,, "`} {
			if _, _, err := Convert(ConvertCSVList, json.RawMessage(head)); !errors.Is(err, ErrNotStaged) {
				t.Fatalf("Convert(csv-list, %s) = %v, want ErrNotStaged", head, err)
			}
		}
		ok(t, ConvertCSVList, `[]`, `""`)
		ok(t, ConvertCSVList, `["Gotham"]`, `"Gotham"`)
		ok(t, ConvertCSVList, `[" Gotham ","Metropolis"]`, `"Gotham,Metropolis"`)
		refused(t, ConvertCSVList, `["Gotham,Metropolis"]`, ErrNotRepresentable)
		refused(t, ConvertCSVList, `["Gotham",""]`, ErrNotRepresentable)
		refused(t, ConvertCSVList, `["  "]`, ErrNotRepresentable)
		refused(t, ConvertCSVList, `"Gotham"`, ErrConvertInput)
		refused(t, ConvertCSVList, `[1]`, ErrConvertInput)
	})
	t.Run("never-stored: nothing to give back", func(t *testing.T) {
		refused(t, ConvertNeverStored, `"x"`, ErrNotApplicable)
	})
	t.Run("days-to-duration: only whole days are HEAD's", func(t *testing.T) {
		// Forward accepts 1.5 days and writes 36h; HEAD's field is an integer.
		canon, _, err := Convert(ConvertDaysToDuration, json.RawMessage(`1.5`))
		if err != nil || string(canon) != `"36h"` {
			t.Fatalf("Convert(1.5) = %s, %v", canon, err)
		}
		refused(t, ConvertDaysToDuration, `"36h"`, ErrNotRepresentable)
		refused(t, ConvertDaysToDuration, `"1h"`, ErrNotRepresentable)
		refused(t, ConvertDaysToDuration, `"24h1s"`, ErrNotRepresentable)
		refused(t, ConvertDaysToDuration, `"soon"`, ErrConvertInput)
		refused(t, ConvertDaysToDuration, `7`, ErrConvertInput)
		ok(t, ConvertDaysToDuration, `"48h"`, `2`)
		ok(t, ConvertDaysToDuration, `"0s"`, `0`)
		ok(t, ConvertDaysToDuration, `"2160h"`, `90`)
	})
	t.Run("mib-to-bytes: only whole MiB are HEAD's", func(t *testing.T) {
		ok(t, ConvertMiBToBytes, `8388608`, `8`)
		ok(t, ConvertMiBToBytes, `0`, `0`)
		refused(t, ConvertMiBToBytes, `8388609`, ErrNotRepresentable)
		refused(t, ConvertMiBToBytes, `1048575`, ErrNotRepresentable)
		refused(t, ConvertMiBToBytes, `1.5`, ErrNotRepresentable)
		refused(t, ConvertMiBToBytes, `"8"`, ErrConvertInput)
	})
	t.Run("auth and updater mode: only the two values HEAD has", func(t *testing.T) {
		ok(t, ConvertAuthMode, `"required"`, `true`)
		ok(t, ConvertAuthMode, `"disabled"`, `false`)
		refused(t, ConvertAuthMode, `"optional"`, ErrNotRepresentable)
		refused(t, ConvertAuthMode, `true`, ErrConvertInput)
		ok(t, ConvertUpdaterMode, `"check"`, `true`)
		ok(t, ConvertUpdaterMode, `"off"`, `false`)
		refused(t, ConvertUpdaterMode, `"apply"`, ErrNotRepresentable)
		refused(t, ConvertUpdaterMode, `false`, ErrConvertInput)
	})
	t.Run("channel: prerelease is develop, anything else passes through", func(t *testing.T) {
		ok(t, ConvertUpdaterChannel, `"prerelease"`, `"develop"`)
		ok(t, ConvertUpdaterChannel, `"stable"`, `"stable"`)
		ok(t, ConvertUpdaterChannel, `"beta"`, `"beta"`)
		refused(t, ConvertUpdaterChannel, `1`, ErrConvertInput)
	})
	t.Run("identity", func(t *testing.T) {
		ok(t, "", `{"a":[1,2]}`, `{"a":[1,2]}`)
	})
}

// ---- the OIDC fold ----

// forwardFold runs the real forward fold (planner.foldValue and fold) over the
// HEAD oauth_* values and returns the providers value it stages, nil when it
// stages none.
func forwardFold(t *testing.T, head map[string]any, secretsImported bool) json.RawMessage {
	t.Helper()
	pl := &planner{p: &plan{Counts: map[string]int{}}, imported: map[secrets.Name]bool{}}
	if secretsImported {
		for _, p := range oidcProviders {
			pl.imported[secrets.Name(p.secret)] = true
		}
	}
	names := make([]string, 0, len(head))
	for k := range head {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		pl.foldValue(k, head[k])
	}
	pl.fold()
	for _, s := range pl.p.Stages {
		if s.Path == "core.auth.oidc.providers" {
			return s.Value
		}
	}
	return nil
}

func rawStr(s string) json.RawMessage { return mustJSON(s) }

func TestUnfoldOIDC_RoundTripsThroughTheFold(t *testing.T) {
	cases := []struct {
		name string
		head map[string]any
		want map[string]json.RawMessage
		// lossy names what the fold loses.
		lossy string
	}{
		{name: "both providers", head: map[string]any{
			"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_google_client_id": "go-robin",
			"oauth_allowed_emails": "bruce@batcave.example,alfred@batcave.example", "oauth_default_role": "editor"},
			want: map[string]json.RawMessage{
				"oauth_enabled": json.RawMessage(`true`), "oauth_github_client_id": rawStr("gh-batman"), "oauth_google_client_id": rawStr("go-robin"),
				"oauth_allowed_emails": rawStr("bruce@batcave.example,alfred@batcave.example"), "oauth_default_role": rawStr("editor")}},
		{name: "github only, default role, no emails", head: map[string]any{
			"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_default_role": "viewer"},
			want: map[string]json.RawMessage{
				"oauth_enabled": json.RawMessage(`true`), "oauth_github_client_id": rawStr("gh-batman"), "oauth_google_client_id": rawStr(""),
				"oauth_allowed_emails": rawStr(""), "oauth_default_role": rawStr("viewer")}},
		{name: "google only, no role", head: map[string]any{
			"oauth_enabled": true, "oauth_google_client_id": "go-robin"},
			want: map[string]json.RawMessage{
				"oauth_enabled": json.RawMessage(`true`), "oauth_github_client_id": rawStr(""), "oauth_google_client_id": rawStr("go-robin"),
				"oauth_allowed_emails": rawStr("")},
			lossy: "an entry with no default_role leaves oauth_default_role out"},
		{name: "emails with spacing and empties", head: map[string]any{
			"oauth_enabled": true, "oauth_github_client_id": "gh-batman", "oauth_allowed_emails": " bruce@batcave.example , ,alfred@batcave.example"},
			want: map[string]json.RawMessage{
				"oauth_enabled": json.RawMessage(`true`), "oauth_github_client_id": rawStr("gh-batman"), "oauth_google_client_id": rawStr(""),
				"oauth_allowed_emails": rawStr("bruce@batcave.example,alfred@batcave.example")},
			lossy: "emails: empties dropped, spacing normalised to a bare comma"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entries := forwardFold(t, c.head, true)
			if entries == nil {
				t.Fatal("the fold staged no providers")
			}
			got, err := UnfoldOIDC(entries)
			if err != nil {
				t.Fatalf("UnfoldOIDC(%s): %v", entries, err)
			}
			assertRawMap(t, got, c.want)
		})
	}
}

func assertRawMap(t *testing.T, got, want map[string]json.RawMessage) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("keys = %v, want %v", rawKeys(got), rawKeys(want))
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("missing %s", k)
			continue
		}
		if !jsonEq(t, string(g), string(w)) {
			t.Errorf("%s = %s, want %s", k, g, w)
		}
	}
}

func rawKeys(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestUnfoldOIDC_NoEntriesMeansSignInOff(t *testing.T) {
	// The fold stages nothing when sign-in is off (an entry would turn SSO on),
	// so an empty list is off, and says nothing about the ids HEAD keeps.
	if forwardFold(t, map[string]any{"oauth_enabled": false, "oauth_github_client_id": "gh-batman"}, true) != nil {
		t.Fatal("the fold staged providers with sign-in off")
	}
	for _, in := range []string{`[]`, `null`} {
		got, err := UnfoldOIDC(json.RawMessage(in))
		if err != nil {
			t.Fatalf("UnfoldOIDC(%s): %v", in, err)
		}
		assertRawMap(t, got, map[string]json.RawMessage{"oauth_enabled": json.RawMessage(`false`)})
	}
}

func TestUnfoldOIDC_Refusals(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     error
	}{
		{"not a list", `{"id":"github"}`, ErrConvertInput},
		{"an entry that is not an object", `[1]`, ErrConvertInput},
		{"unknown provider", `[{"id":"wayne-sso","client_id":"x"}]`, ErrNotRepresentable},
		{"duplicate provider", `[{"id":"github","client_id":"a"},{"id":"github","client_id":"b"}]`, ErrNotRepresentable},
		{"no client id", `[{"id":"github"}]`, ErrNotRepresentable},
		{"emails differ", `[{"id":"github","client_id":"a","allowed_emails":["x@batcave.example"]},{"id":"google","client_id":"b","allowed_emails":["y@batcave.example"]}]`, ErrNotRepresentable},
		{"roles differ", `[{"id":"github","client_id":"a","default_role":"editor"},{"id":"google","client_id":"b","default_role":"viewer"}]`, ErrNotRepresentable},
		{"one entry has emails and the other none", `[{"id":"github","client_id":"a","allowed_emails":["x@batcave.example"]},{"id":"google","client_id":"b"}]`, ErrNotRepresentable},
		{"one entry has a role and the other none", `[{"id":"github","client_id":"a","default_role":"editor"},{"id":"google","client_id":"b"}]`, ErrNotRepresentable},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := UnfoldOIDC(json.RawMessage(c.in))
			if !errors.Is(err, c.want) {
				t.Fatalf("UnfoldOIDC = %v, %v; want %v", got, err, c.want)
			}
			if got != nil {
				t.Errorf("a refusal returned a map: %v", got)
			}
			if strings.Contains(err.Error(), "batcave") {
				t.Errorf("the error carries a value: %v", err)
			}
		})
	}
}

func TestUnfoldOIDC_ExtrasAreIgnored(t *testing.T) {
	// kind, issuer and the client_secret reference carry nothing HEAD holds in an
	// oauth_* field (the secret itself is class s and moves by name).
	got, err := UnfoldOIDC(json.RawMessage(`[{"id":"google","kind":"oidc","issuer":"https://accounts.example","client_id":"go-robin","client_secret":{"secret":"core.auth.oidc.google.client_secret"},"scopes":["a"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	assertRawMap(t, got, map[string]json.RawMessage{
		"oauth_enabled": json.RawMessage(`true`), "oauth_github_client_id": rawStr(""), "oauth_google_client_id": rawStr("go-robin"),
		"oauth_allowed_emails": rawStr("")})
}

// ---- the library roots ----

// forwardRoots mirrors what M6 (F7-08, internal/books/cutover/m6/roots.go,
// PR #4755 at 51d3ed94e) stages for root_dir, path_aliases and the
// import_path rows: library.RootSpec values, in plan order (books-library,
// then the import roots by ID), each path_aliases member attached to the
// root whose cleaned Path equals the alias's cleaned Root.
//
// TODO(F7-08): once M6 merges, replace this mirror with the real forward
// (m6's roots pass) so a drift in the entry shape fails here.
func forwardRoots(t *testing.T, rootDir string, imports map[int]string, aliases []headPathAlias) json.RawMessage {
	t.Helper()
	var specs []library.RootSpec
	if rootDir != "" {
		specs = append(specs, library.RootSpec{
			ID: "books-library", Path: rootDir, Role: library.RoleLibrary, Owner: "books",
			Watch: library.WatchOff, Sentinel: library.SentinelBootstrap, MinFree: 0,
			Quarantine: ".failed", AppDir: library.DefaultAppDir,
		})
	}
	ids := make([]int, 0, len(imports))
	for n := range imports {
		ids = append(ids, n)
	}
	sort.Ints(ids)
	for _, n := range ids {
		specs = append(specs, library.RootSpec{
			ID: library.RootID("import-" + strconv.Itoa(n)), Path: imports[n], Role: library.RoleImport, Owner: "books",
			Watch: library.WatchAuto, Sentinel: library.SentinelBootstrap, MinFree: library.MinFreeDefault,
		})
	}
	for _, a := range aliases {
		for i := range specs {
			if a.Root != "" && filepath.Clean(specs[i].Path) == filepath.Clean(a.Root) {
				specs[i].Aliases = append(specs[i].Aliases, library.Alias{Windows: a.Windows, UNC: a.UNC, SMBURL: a.SMBURL})
				break
			}
		}
	}
	b, err := json.Marshal(specs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnfoldRoots_RoundTripsThroughTheRootsEntry(t *testing.T) {
	archive := headPathAlias{Root: "/gotham/archive", Windows: "W:", UNC: `\\wayne\archive`, SMBURL: "smb://wayne/archive"}
	import1 := headPathAlias{Root: "/gotham/imports/one", Windows: "X:", SMBURL: "smb://wayne/one"}
	for _, c := range []struct {
		name    string
		rootDir string
		imports map[int]string
		aliases []headPathAlias
		want    []headPathAlias // nil: same as aliases
		lossy   string
	}{
		{name: "root only", rootDir: "/gotham/archive"},
		{name: "root with an alias", rootDir: "/gotham/archive", aliases: []headPathAlias{archive}},
		{name: "no root", rootDir: ""},
		{name: "import root alias is HEAD's too", rootDir: "/gotham/archive", imports: map[int]string{1: "/gotham/imports/one"},
			aliases: []headPathAlias{archive, import1}},
		{name: "alias order across roots follows the root list", rootDir: "/gotham/archive", imports: map[int]string{1: "/gotham/imports/one"},
			aliases: []headPathAlias{import1, archive}, want: []headPathAlias{archive, import1},
			lossy: "cross-root alias order: aliases come back grouped by root, in root order"},
		{name: "alias root with a trailing slash", rootDir: "/gotham/archive",
			aliases: []headPathAlias{{Root: "/gotham/archive/", Windows: "W:"}},
			want:    []headPathAlias{{Root: "/gotham/archive", Windows: "W:"}},
			lossy:   "alias Root comes back as the root's own Path, not HEAD's spelling"},
	} {
		t.Run(c.name, func(t *testing.T) {
			entries := forwardRoots(t, c.rootDir, c.imports, c.aliases)
			dir, aliases, err := UnfoldRoots(entries)
			if err != nil {
				t.Fatalf("UnfoldRoots(%s): %v", entries, err)
			}
			if dir != c.rootDir {
				t.Errorf("rootDir = %q, want %q", dir, c.rootDir)
			}
			want := c.want
			if want == nil {
				want = c.aliases
			}
			if want == nil {
				want = []headPathAlias{}
			}
			var got []headPathAlias
			if err := json.Unmarshal(aliases, &got); err != nil {
				t.Fatalf("aliases %s: %v", aliases, err)
			}
			if got == nil {
				got = []headPathAlias{}
			}
			if !slices.Equal(got, want) {
				t.Errorf("aliases = %+v, want %+v", got, want)
			}
			if string(aliases) == "null" {
				t.Errorf("aliases is JSON null, want a list")
			}
		})
	}
}

func TestUnfoldRoots_NoBooksRoot(t *testing.T) {
	// A list with no books-library entry: HEAD has no root_dir. An import root's
	// own aliases still belong to HEAD's path_aliases.
	entries := forwardRoots(t, "", map[int]string{1: "/gotham/imports/one"}, []headPathAlias{{Root: "/gotham/imports/one", Windows: "X:"}})
	dir, aliases, err := UnfoldRoots(entries)
	if err != nil || dir != "" {
		t.Fatalf("dir = %q, err = %v", dir, err)
	}
	var got []headPathAlias
	if err := json.Unmarshal(aliases, &got); err != nil || len(got) != 1 || got[0].Root != "/gotham/imports/one" || got[0].Windows != "X:" {
		t.Errorf("aliases = %s, %v", aliases, err)
	}
	for _, in := range []string{`[]`, `null`} {
		dir, aliases, err := UnfoldRoots(json.RawMessage(in))
		if err != nil || dir != "" || string(aliases) != `[]` {
			t.Errorf("UnfoldRoots(%s) = %q, %s, %v", in, dir, aliases, err)
		}
	}
}

func TestUnfoldRoots_Refusals(t *testing.T) {
	for _, c := range []struct{ name, in string }{
		{"not a list", `{"id":"books-library"}`},
		{"an entry that is not an object", `[1]`},
		{"two books-library entries", `[{"id":"books-library","path":"/a"},{"id":"books-library","path":"/b"}]`},
		{"path not a string", `[{"id":"books-library","path":1}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, aliases, err := UnfoldRoots(json.RawMessage(c.in))
			if !errors.Is(err, ErrConvertInput) {
				t.Fatalf("UnfoldRoots = %q, %s, %v; want ErrConvertInput", dir, aliases, err)
			}
			if dir != "" || aliases != nil {
				t.Errorf("a refusal returned values: %q %s", dir, aliases)
			}
		})
	}
}
