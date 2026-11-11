// file: internal/books/cutover/rename_convert_test.go
// version: 1.0.0
// guid: 8a1f4c6e-0e6b-4a93-a8b1-4f1a2c3d9e03
// last-edited: 2026-11-11

package cutover

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// jsonEq compares two JSON documents by value.
func jsonEq(t testing.TB, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("bad JSON %q: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("bad JSON %q: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}

// TestConvert_Table: each converter's input and output (05-config §1.12.3's
// Convert column).
func TestConvert_Table(t *testing.T) {
	cases := []struct {
		name, conv, in, want string
		dropped              []string
	}{
		{"identity", "", `"The Long Halloween"`, `"The Long Halloween"`, nil},
		{"auth on", ConvertAuthMode, `true`, `"required"`, nil},
		{"auth off", ConvertAuthMode, `false`, `"disabled"`, nil},
		{"days", ConvertDaysToDuration, `7`, `"168h"`, nil},
		{"zero days", ConvertDaysToDuration, `0`, `"0s"`, nil},
		{"mib", ConvertMiBToBytes, `4`, `4194304`, nil},
		{"log retention keeps", ConvertLogRetention, `30`, `30`, nil},
		{"log retention negative is forever", ConvertLogRetention, `-1`, `0`, nil},
		{"updater on", ConvertUpdaterMode, `true`, `"check"`, nil},
		{"updater off", ConvertUpdaterMode, `false`, `"off"`, nil},
		{"develop is prerelease", ConvertUpdaterChannel, `"develop"`, `"prerelease"`, nil},
		{"stable stays", ConvertUpdaterChannel, `"stable"`, `"stable"`, nil},
		{"check every 90 minutes", ConvertUpdaterCheck, `90`, `{"enabled":true,"every":"1h30m"}`, nil},
		{"standard schedule", ConvertScheduleStandard, `{"enabled":true,"interval":60,"on_startup":false}`, `{"enabled":true,"every":"1h"}`, nil},
		{"standard schedule reports on_startup", ConvertScheduleStandard, `{"enabled":true,"interval":30,"on_startup":true}`, `{"enabled":true,"every":"30m"}`, []string{"on_startup"}},
		{"standard schedule disabled", ConvertScheduleStandard, `{"enabled":false,"interval":0}`, `{"enabled":false}`, nil},
		{"interval-only runs above zero", ConvertScheduleInterval, `{"enabled":false,"interval":720}`, `{"enabled":true,"every":"12h"}`, nil},
		{"interval-only off at zero", ConvertScheduleInterval, `{"enabled":true,"interval":0}`, `{"enabled":false}`, nil},
		{"full scan", ConvertScheduleFullScan, `{"enabled":true,"period_hours":24}`, `{"enabled":true,"every":"24h"}`, nil},
		{"csv list", ConvertCSVList, `"Gotham, Metropolis ,,Bludhaven"`, `["Gotham","Metropolis","Bludhaven"]`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, dropped, err := Convert(c.conv, json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("Convert(%q, %s): %v", c.conv, c.in, err)
			}
			if !jsonEq(t, string(got), c.want) {
				t.Errorf("Convert(%q, %s) = %s, want %s", c.conv, c.in, got, c.want)
			}
			if !reflect.DeepEqual(dropped, c.dropped) {
				t.Errorf("dropped = %v, want %v", dropped, c.dropped)
			}
		})
	}
}

// TestConvert_NotStaged: the converters whose input means "use the default",
// "never stored" or "folded elsewhere" stage nothing.
func TestConvert_NotStaged(t *testing.T) {
	for _, c := range []struct{ conv, in string }{
		{ConvertNeverStored, `"/var/lib/batcave/gotham.db"`},
		{ConvertOIDCFold, `true`},
		{ConvertUpdaterCheck, `0`},
		{ConvertUpdaterCheck, `-5`},
		{ConvertCSVList, `" , "`},
	} {
		if _, _, err := Convert(c.conv, json.RawMessage(c.in)); !errors.Is(err, ErrNotStaged) {
			t.Errorf("Convert(%q, %s) error = %v, want ErrNotStaged", c.conv, c.in, err)
		}
	}
}

// TestConvert_RefusesTheWrongShapeWithoutTheValue: a converter given the wrong
// JSON type fails with ErrConvertInput, and its message never carries the
// value.
func TestConvert_RefusesTheWrongShapeWithoutTheValue(t *testing.T) {
	const marker = "Penguin-Umbrella-7731"
	for _, conv := range []string{
		ConvertAuthMode, ConvertDaysToDuration, ConvertMiBToBytes, ConvertLogRetention, ConvertUpdaterMode,
		ConvertUpdaterChannel, ConvertUpdaterCheck, ConvertScheduleStandard, ConvertScheduleInterval, ConvertScheduleFullScan, ConvertCSVList,
	} {
		in := json.RawMessage(`["` + marker + `",[1,2]]`)
		_, _, err := Convert(conv, in)
		if !errors.Is(err, ErrConvertInput) {
			t.Errorf("Convert(%q, an array) error = %v, want ErrConvertInput", conv, err)
			continue
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("Convert(%q) error carries the value: %v", conv, err)
		}
	}
	if _, _, err := Convert("no-such-converter", json.RawMessage(`1`)); !errors.Is(err, ErrConvertInput) {
		t.Errorf("an unknown converter's error = %v, want ErrConvertInput", err)
	}
}

// TestConvert_EveryConverterTheTableNamesIsKnown: the table names no converter
// Convert does not implement.
func TestConvert_EveryConverterTheTableNamesIsKnown(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Table().Rows {
		for _, tg := range r.Targets {
			if !knownConvert(tg.Convert) {
				t.Errorf("row %s names the converter %q, which Convert does not implement", r.Head, tg.Convert)
			}
			seen[tg.Convert] = true
		}
	}
	for _, c := range []string{ConvertAuthMode, ConvertDaysToDuration, ConvertMiBToBytes, ConvertLogRetention, ConvertUpdaterMode, ConvertUpdaterChannel, ConvertUpdaterCheck, ConvertScheduleStandard, ConvertCSVList} {
		if !seen[c] {
			t.Errorf("no row of the table uses the converter %q", c)
		}
	}
}
