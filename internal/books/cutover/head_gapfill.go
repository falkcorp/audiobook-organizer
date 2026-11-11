// file: internal/books/cutover/head_gapfill.go
// version: 1.0.0
// guid: 86ad29a6-334d-4bed-aa94-a282e32106c3
// last-edited: 2026-11-11

// FROZEN COPY of HEAD (748fd787a) config.yaml gap-fill reader and managed-key strip; read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

import (
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

// headDropManagedKeys removes from m every managed key and every removed key,
// matching case-insensitively (HEAD's SaveConfigToFile merge: an operator's
// "ROOT_DIR" does not survive beside the managed "root_dir").
func headDropManagedKeys(m map[string]any) {
	for _, rk := range headRemovedKeys {
		headDeleteConfigKeyFold(m, rk.key)
	}
	for _, k := range headManagedFileKeys {
		headDeleteConfigKeyFold(m, k)
	}
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
