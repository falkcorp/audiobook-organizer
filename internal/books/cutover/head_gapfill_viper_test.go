// file: internal/books/cutover/head_gapfill_viper_test.go
// version: 1.0.0
// guid: d30c7726-d9e9-47af-86af-a1f5e2192757
// last-edited: 2026-11-12

package cutover

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// The old build read config.yaml through viper and every managed key through
// v.GetString, v.GetBool or v.GetInt. headParseFileSettings stands in for that
// read, so these tests drive the same YAML through a real viper.New() +
// ReadConfig + the typed getter and compare, for every key of every kind,
// instead of pinning what a port of spf13/cast would answer. A literal viper
// cannot parse must read as empty on our side too (HEAD ignores such a file).

// yamlLit is one YAML scalar or collection, spelled as it would follow
// "key: ". refused marks an int the plan's float64 tree cannot hold exactly:
// headParseFileSettings must name it in its second result, not read it.
type yamlLit struct {
	lit     string
	refused bool
}

func lits(ls ...string) []yamlLit {
	out := make([]yamlLit, len(ls))
	for i, l := range ls {
		out[i] = yamlLit{lit: l}
	}
	return out
}

var diffBoolLits = lits(
	"true", "True", "TRUE", "false", "False", "FALSE", "tRuE",
	"yes", "Yes", "YES", "no", "No", "NO", "on", "On", "ON", "off", "Off", "OFF",
	"y", "Y", "n", "N",
	`"true"`, `'true'`, `"True"`, `"TRUE"`, `"false"`, `"False"`, `"t"`, `"T"`, `"f"`, `"F"`,
	`"1"`, `"0"`, `"2"`, `"-1"`, `"1.0"`, `"yes"`, `"on"`, `" true"`, `"true "`, `'0x1'`,
	"1", "0", "2", "-1", "1.0", "0.0", "0.5", "1e0", "0x1", "0o1", ".inf", "-.inf", ".nan",
	"[]", "[true]", "{}", "{alfred: true}",
	`""`, `''`, "~", "null", "Null", "NULL", "",
	"2001-12-14", "2001-12-14T21:59:43.10-05:00",
	"!!str true", "!!bool true", "!!binary aGVsbG8=",
)

var diffStringLits = lits(
	"Gotham", `"Gotham"`, `'Gotham'`, "wayne-manor", `"{author}/{title}"`, `'{title} - {track:02d}'`, "/mnt/gotham-playlists",
	"7", "007", "0x1F", "1_000", "1e3", "2.5", "3.0", "-1", "+1", "1e21", "1.5e300", "0.1", "100000000000000000000",
	".inf", "-.inf", ".nan",
	"true", "True", "false", "yes", "no", "on", "off", "y", "n",
	"~", "null", "Null", "NULL", "", `""`, `''`,
	"[]", "[alfred]", "[1, 2]", "{}", "{alfred: butler}",
	"2001-12-14", "2001-12-14T21:59:43.10-05:00", "2001-12-14 21:59:43.10 -5", "2001-12-15 2:59:43.10",
	"!!str 10", "!!int 10", "!!float 1", "!!binary aGVsbG8=",
	"|\n  alpha\n  beta", ">\n  alpha\n  beta", `"multi\nline"`,
)

var diffIntLits = []yamlLit{
	{"0", false}, {"1", false}, {"-1", false}, {"+1", false}, {"10", false}, {"007", false}, {"0o17", false},
	{"0x1F", false}, {"0b101", false}, {"1_000", false},
	{`"10"`, false}, {`"10.7"`, false}, {`"+10.9"`, false}, {`"-10.5"`, false}, {`"10.0"`, false}, {`"10."`, false},
	{`".5"`, false}, {`"-.5"`, false}, {`"1e3"`, false}, {`"0.99"`, false}, {`"0x10"`, false}, {`"0X1f"`, false},
	{`"0b11"`, false}, {`"0o7"`, false}, {`"07"`, false}, {`"09"`, false}, {`"1 000"`, false}, {`" 10"`, false},
	{`"10 "`, false}, {`"abc"`, false}, {`"many"`, false}, {`""`, false}, {`''`, false}, {`".inf"`, false},
	{`"2001-12-14"`, false}, {"!!str 10", false},
	{"1e3", false}, {"1.5e3", false}, {"3.9", false}, {"-3.9", false}, {"3.5", false}, {"2.5", false}, {"0.99", false},
	{"true", false}, {"false", false}, {"[]", false}, {"[1]", false}, {"{}", false}, {"{alfred: 1}", false},
	{"2001-12-14", false}, {"!!binary aGVsbG8=", false},
	{"~", false}, {"null", false}, {"", false},
	// Exactly 2^53 is the last magnitude a float64 holds, so it is carried.
	{"9007199254740992", false}, {`"9007199254740992"`, false}, {"9007199254740992.0", false},
	{"-9007199254740992", false}, {`"-9007199254740992"`, false},
	// A number the tree cannot hold, or one the platform converts by its own
	// rule, is refused by name: reading it as absent would delete a value.
	{"9007199254740993", true}, {`"9007199254740993"`, true}, {"-9007199254740993", true}, {`"-9007199254740993"`, true},
	{"9007199254740994.0", true}, {"9223372036854775807", true}, {"9223372036854775808", true},
	{"18446744073709551615", true}, {"18446744073709551616", true}, {"99999999999999999999", true},
	{"1e19", true}, {"1e30", true}, {"-1e30", true}, {`"0x7fffffffffffffff"`, true},
	{".inf", true}, {"-.inf", true}, {".nan", true},
	// A string cast.ToInt cannot parse reads 0 everywhere and is faithful.
	{`"99999999999999999999"`, false}, {`"1e400"`, false},
}

func TestHeadParseFileSettings_DifferentialAgainstViper(t *testing.T) {
	byKind := map[string][]yamlLit{"bool": diffBoolLits, "int": diffIntLits, "string": diffStringLits}
	for kind, ls := range byKind {
		if len(ls) < 40 {
			t.Fatalf("%s has %d literals, want at least 40", kind, len(ls))
		}
	}
	covered := 0
	for _, key := range slices.Sorted(maps.Keys(headViperKinds)) {
		kind := headViperKinds[key]
		ls, ok := byKind[kind]
		if !ok {
			t.Fatalf("%s is read as kind %q, which the differential has no literals for", key, kind)
		}
		t.Run(key, func(t *testing.T) {
			for _, l := range ls {
				covered++
				doc := key + ": " + l.lit + "\n"
				v := viper.New()
				v.SetConfigType("yaml")
				rerr := v.ReadConfig(strings.NewReader(doc))
				got, unrep := headParseFileSettings([]byte(doc))
				_, has := got[key]
				switch {
				case rerr != nil:
					if has || unrep[key] {
						t.Errorf("%q: viper cannot parse it (%v), we read %v (refused %v)", doc, rerr, got, unrep)
					}
				case !v.IsSet(key):
					if has || unrep[key] {
						t.Errorf("%q: viper reads the key as unset, we read %v (refused %v)", doc, got, unrep)
					}
				case kind == "int" && l.refused:
					if has || !unrep[key] {
						t.Errorf("%q: a number the tree cannot hold must be refused by name, we read %v (refused %v)", doc, got, unrep)
					}
				default:
					if unrep[key] {
						t.Errorf("%q: refused, but viper's reading is carried exactly", doc)
						continue
					}
					var want any
					switch kind {
					case "bool":
						want = v.GetBool(key)
					case "int":
						want = float64(v.GetInt(key))
					case "string":
						want = v.GetString(key)
					}
					if !has || got[key] != want {
						t.Errorf("%q: viper's %s getter reads %#v, we read %#v (present %v)", doc, kind, want, got[key], has)
					}
				}
			}
		})
	}
	if covered < 10*40 {
		t.Errorf("the differential ran %d cells, want at least %d", covered, 10*40)
	}
}

// The review's table: cast.ToInt truncates any decimal (spf13/cast v1.10.0
// trimDecimal), so a quoted fraction is not zero.
func TestHeadParseFileSettings_TruncatesAnyDecimalLikeCast(t *testing.T) {
	for lit, want := range map[string]float64{
		`"10.7"`: 10, `"+10.9"`: 10, `"-10.5"`: -10, `"3.5"`: 3, `"0.99"`: 0, `"-0.99"`: 0,
		"10.7": 10, "-10.5": -10, "3.9": 3, "-3.9": -3, "2.5": 2,
	} {
		got, unrep := headParseFileSettings([]byte("concurrent_scans: " + lit + "\n"))
		if got["concurrent_scans"] != want || len(unrep) != 0 {
			t.Errorf("concurrent_scans: %s read %v (refused %v), want %v", lit, got, unrep, want)
		}
	}
}

// The parser's key sets are one list seen three ways: the keys it reads, the
// type each is read as, and the keys whose HEAD default is computed. They must
// agree, or a key is read with no cast, compared with no default, or defaulted
// from a table that cannot hold it.
func TestHeadViperKeySetsAgree(t *testing.T) {
	files := slices.Clone(headViperFileKeys)
	slices.Sort(files)
	if got := slices.Compact(slices.Clone(files)); len(got) != len(files) {
		t.Errorf("headViperFileKeys repeats a key: %v", headViperFileKeys)
	}
	if kinds := slices.Sorted(maps.Keys(headViperKinds)); !slices.Equal(files, kinds) {
		t.Errorf("headViperFileKeys %v and headViperKinds %v name different keys", files, kinds)
	}
	for k, kind := range headViperKinds {
		if kind != "bool" && kind != "int" && kind != "string" {
			t.Errorf("%s is read as %q, which headCastFileValue does not cast", k, kind)
		}
	}
	for _, k := range headComputedDefaultNames {
		if !slices.Contains(headViperFileKeys, k) {
			t.Errorf("%s has a computed default but is not a viper-read key", k)
		}
		if _, ok := headComputedDefault(k); !ok {
			t.Errorf("headComputedDefaultNames lists %s, which headComputedDefault does not answer", k)
		}
	}
	for _, k := range headViperFileKeys {
		if _, ok := headDefault(k); !ok {
			t.Errorf("%s is a viper-read key with no HEAD default, so headIsDefault could never match it", k)
		}
		if !slices.Contains(headComputedDefaultNames, k) {
			if _, ok := headComputedDefault(k); ok {
				t.Errorf("headComputedDefault answers %s, which headComputedDefaultNames does not list", k)
			}
		}
	}
}

// Two spellings of one key: viper lowercases both and its pick is map-order
// luck, so M2 takes the later in sorted order, deterministically, and the
// verdict (read or refused) follows the one it takes.
func TestHeadParseFileSettings_TwoSpellingsOfOneKey(t *testing.T) {
	got, unrep := headParseFileSettings([]byte("CONCURRENT_SCANS: .inf\nconcurrent_scans: 3\n"))
	if got["concurrent_scans"] != float64(3) || len(unrep) != 0 {
		t.Errorf("a refusable spelling before a good one read %v (refused %v), want 3 and no refusal", got, unrep)
	}
	got, unrep = headParseFileSettings([]byte("CONCURRENT_SCANS: 3\nconcurrent_scans: .inf\n"))
	if _, has := got["concurrent_scans"]; has || !unrep["concurrent_scans"] {
		t.Errorf("a good spelling before a refusable one read %v (refused %v), want it refused", got, unrep)
	}
}
