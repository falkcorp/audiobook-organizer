// file: internal/books/cutover/head_gapfill.go
// version: 1.1.0
// guid: 86ad29a6-334d-4bed-aa94-a282e32106c3
// last-edited: 2026-11-11

// FROZEN COPY of HEAD (748fd787a) config.yaml gap-fill reader and managed-key strip; read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// headManagedFileKeys are exactly the keys HEAD's SaveConfigToFile manages in
// config.yaml: twelve settings, then three secrets (fifteen). A strip of the
// operator's file drops each of them and every removed key.
var headManagedFileKeys = []string{
	"root_dir", "database_path", "playlist_dir", "setup_complete",
	"organization_strategy", "scan_on_startup", "auto_organize",
	"folder_naming_pattern", "file_naming_pattern", "enable_ai_parsing",
	"concurrent_scans", "log_level",
	"openai_api_key", "google_books_api_key", "hardcover_api_token",
}

// headFileValues is what HEAD's LoadConfigFromFile reads from config.yaml:
// the three secrets and root_dir (each applied only where the stored value is
// empty) and enable_ai_parsing (applied only when stored false and the file
// says true).
type headFileValues struct {
	OpenAIAPIKey      string
	GoogleBooksAPIKey string
	HardcoverAPIToken string
	RootDir           string
	EnableAIParsing   bool
}

// headParseGapFill reads the five keys HEAD's gap-fill reads. The YAML is
// unmarshalled into map[string]any like HEAD, and a key is read by exact
// (case-sensitive) name. An unparsable file returns zero values, a nil error
// and a nil present map (HEAD logs it and ignores the file). present records
// which of the five keys carried a usable value: a non-empty string, or true.
func headParseGapFill(data []byte) (headFileValues, map[string]bool, error) {
	var fileConfig map[string]any
	if err := yaml.Unmarshal(data, &fileConfig); err != nil {
		return headFileValues{}, nil, nil
	}
	var v headFileValues
	present := map[string]bool{}
	for _, s := range []struct {
		key string
		ptr *string
	}{
		{"openai_api_key", &v.OpenAIAPIKey},
		{"google_books_api_key", &v.GoogleBooksAPIKey},
		{"hardcover_api_token", &v.HardcoverAPIToken},
		{"root_dir", &v.RootDir},
	} {
		if val, ok := fileConfig[s.key].(string); ok && val != "" {
			*s.ptr = val
			present[s.key] = true
		}
	}
	if val, ok := fileConfig["enable_ai_parsing"].(bool); ok && val {
		v.EnableAIParsing = true
		present["enable_ai_parsing"] = true
	}
	return v, present, nil
}

// headViperFileKeys are the managed keys, other than the gap-fill secrets and
// root_dir, that configFromViper reads from a config.yaml: HEAD's root command
// points viper at the file (cmd/root.go: SetConfigFile, ReadInConfig, and
// docs/configuration.md documents --config at the file beside the database),
// so with no settings blob they are the effective settings. enable_ai_parsing
// is here as well as in the gap-fill five: with no blob its file value is
// effective whichever way it points. database_path is not here: M2 never
// stores it (the rename table's convert=never-stored, core.storage.path is
// operator-layer), blob or no blob, and the store being cut over is already at
// that path, so a file value of it loses nothing.
var headViperFileKeys = []string{
	"playlist_dir", "setup_complete", "organization_strategy", "scan_on_startup",
	"auto_organize", "folder_naming_pattern", "file_naming_pattern",
	"enable_ai_parsing", "concurrent_scans", "log_level",
}

// headViperKinds is the type configFromViper reads each headViperFileKeys key
// as: v.GetString, v.GetBool or v.GetInt.
var headViperKinds = map[string]string{
	"playlist_dir": "string", "setup_complete": "bool", "organization_strategy": "string",
	"scan_on_startup": "bool", "auto_organize": "bool", "folder_naming_pattern": "string",
	"file_naming_pattern": "string", "enable_ai_parsing": "bool", "concurrent_scans": "int",
	"log_level": "string",
}

// headViperCast converts a file value (decoded JSON: a float64 for any number)
// to what configFromViper gets from it, which is spf13/cast's reading of it
// through the key's typed getter: "yes" is false for a bool (ParseBool refuses
// it), "10" is 10 for an int, a list is "" for a string. The blob member it
// stands in for has that type, and the census compares that value with HEAD's
// default, so a file that spells a default another way loses nothing and is
// not refused.
func headViperCast(key string, v any) any {
	switch headViperKinds[key] {
	case "bool":
		switch x := v.(type) {
		case bool:
			return x
		case string:
			b, _ := strconv.ParseBool(x)
			return b
		case float64:
			return x != 0
		}
		return false
	case "int":
		switch x := v.(type) {
		case float64:
			return float64(int(x))
		case string:
			n, err := strconv.ParseInt(headTrimZeroDecimal(x), 0, 0)
			if err != nil {
				return float64(0)
			}
			return float64(n)
		case bool:
			if x {
				return float64(1)
			}
		}
		return float64(0)
	case "string":
		switch x := v.(type) {
		case string:
			return x
		case bool:
			return strconv.FormatBool(x)
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return ""
	}
	return v
}

// headTrimZeroDecimal drops a ".0", ".00" ... suffix, as spf13/cast does
// before it parses an int from a string.
func headTrimZeroDecimal(s string) string {
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return s
	}
	for _, c := range s[i+1:] {
		if c != '0' {
			return s
		}
	}
	return s[:i]
}

// headParseFileSettings reads headViperFileKeys from config.yaml the way
// viper does: a key matches case-insensitively, and a null is an unset key.
// Each value is normalised through JSON (a YAML int becomes a float64) and
// then cast to the type configFromViper reads the key as (headViperCast), so
// headIsDefault compares it with HEAD's default by value and the planner can
// stage it as the blob member it stands in for. An unparsable file reads as
// empty, as HEAD ignores it. Names only are used by callers' reports, never
// the value.
func headParseFileSettings(data []byte) map[string]any {
	var fileConfig map[string]any
	if err := yaml.Unmarshal(data, &fileConfig); err != nil {
		return nil
	}
	names := make([]string, 0, len(fileConfig))
	for k := range fileConfig {
		names = append(names, k)
	}
	sort.Strings(names) // two spellings of one key: the later one wins, deterministically
	out := map[string]any{}
	for _, k := range names {
		lower := strings.ToLower(k)
		known := false
		for _, want := range headViperFileKeys {
			if lower == want {
				known = true
			}
		}
		if !known || fileConfig[k] == nil {
			continue
		}
		raw, err := json.Marshal(fileConfig[k])
		if err != nil {
			continue
		}
		var v any
		if json.Unmarshal(raw, &v) == nil {
			out[lower] = headViperCast(lower, v)
		}
	}
	return out
}

// headDeleteConfigKeyFold removes key from m, matching every path segment
// case-insensitively. A dotted key ("a.b") removes the literal top-level key
// "a.b" and the nested path a -> b; a map left empty by the removal is removed too.
func headDeleteConfigKeyFold(m map[string]any, key string) {
	for k := range m {
		if strings.EqualFold(k, key) {
			delete(m, k)
		}
	}
	parts := strings.Split(key, ".")
	if len(parts) < 2 {
		return
	}
	var walk func(cur map[string]any, parts []string)
	walk = func(cur map[string]any, parts []string) {
		for k, v := range cur {
			if !strings.EqualFold(k, parts[0]) {
				continue
			}
			if len(parts) == 1 {
				delete(cur, k)
				continue
			}
			if child, ok := v.(map[string]any); ok {
				walk(child, parts[1:])
				if len(child) == 0 {
					delete(cur, k)
				}
			}
		}
	}
	walk(m, parts)
}
