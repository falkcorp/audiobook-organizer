// file: internal/books/cutover/head_gapfill.go
// version: 1.2.0
// guid: 86ad29a6-334d-4bed-aa94-a282e32106c3
// last-edited: 2026-11-12

// FROZEN COPY of HEAD (748fd787a) config.yaml gap-fill reader and managed-key strip; read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

import (
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cast"
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

// headMaxExactInt is the largest magnitude a float64 holds exactly. The plan's
// tree carries every number as a float64, so an int beyond it cannot be staged
// as the value HEAD read.
const headMaxExactInt = 1 << 53

// headCastFileValue returns what configFromViper gets for key from the decoded
// YAML value raw. viper's v.GetString, v.GetBool and v.GetInt are
// cast.ToString, cast.ToBool and cast.ToInt of the file's value (viper v1.21.0
// over spf13/cast v1.10.0), so this calls the same functions rather than
// porting them: "10.7" is 10, "yes" is false for a bool, a list is "" for a
// string, a YAML timestamp is time.Time's String form. The blob member it
// stands in for has that type, and the census compares the result with HEAD's
// default, so a file that spells a default another way loses nothing and is
// not refused. ok is false for an int the plan's float64 tree cannot hold
// exactly (headIntIsExact); such a value is refused, never read as absent.
func headCastFileValue(key string, raw any) (v any, ok bool) {
	switch headViperKinds[key] {
	case "bool":
		return cast.ToBool(raw), true
	case "int":
		n := cast.ToInt(raw)
		if !headIntIsExact(raw, n) {
			return nil, false
		}
		return float64(n), true
	case "string":
		return cast.ToString(raw), true
	}
	return raw, true
}

// headIntIsExact reports whether n, cast.ToInt's reading of raw, is the value
// HEAD read and fits a float64 exactly. A float that is not finite, or beyond
// 2^53, converts to an int by the platform's rule (Go leaves it
// implementation-defined), and a uint64 past 2^53 wraps, so none of them is a
// faithful number; an int string cast.ToInt cannot parse reads 0 on every
// platform and is faithful as it is.
func headIntIsExact(raw any, n int) bool {
	switch x := raw.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > headMaxExactInt {
			return false
		}
	case float32:
		if f := float64(x); math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > headMaxExactInt {
			return false
		}
	case uint:
		if x > headMaxExactInt {
			return false
		}
	case uint64:
		if x > headMaxExactInt {
			return false
		}
	}
	return n >= -headMaxExactInt && n <= headMaxExactInt
}

// headParseFileSettings reads headViperFileKeys from config.yaml the way
// viper does: a key matches case-insensitively, and a null is an unset key.
// Each value is cast to the type configFromViper reads the key as
// (headCastFileValue), so headIsDefault compares it with HEAD's default by
// value and the planner can stage it as the blob member it stands in for. A
// key whose value cannot be carried exactly is not in the settings and is
// named in unrepresentable instead: a caller must refuse it, because reading
// it as absent would let the strip delete a value nothing stored. An
// unparsable file reads as empty, as HEAD ignores it. Names only are used by
// callers' reports, never the value.
func headParseFileSettings(data []byte) (settings map[string]any, unrepresentable map[string]bool) {
	var fileConfig map[string]any
	if err := yaml.Unmarshal(data, &fileConfig); err != nil {
		return nil, nil
	}
	names := make([]string, 0, len(fileConfig))
	for k := range fileConfig {
		names = append(names, k)
	}
	sort.Strings(names) // two spellings of one key: the later one wins, deterministically
	settings = map[string]any{}
	unrepresentable = map[string]bool{}
	for _, k := range names {
		lower := strings.ToLower(k)
		if !slices.Contains(headViperFileKeys, lower) || fileConfig[k] == nil {
			continue
		}
		delete(settings, lower)
		delete(unrepresentable, lower)
		if v, ok := headCastFileValue(lower, fileConfig[k]); ok {
			settings[lower] = v
		} else {
			unrepresentable[lower] = true
		}
	}
	return settings, unrepresentable
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
