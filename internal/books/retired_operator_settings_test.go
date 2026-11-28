// file: internal/books/retired_operator_settings_test.go
// version: 0.1.0
// guid: 3f6c1b82-9a47-4d05-b8e3-7c20d4a91e5f
// last-edited: 2026-11-28

package books

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	coreconfig "github.com/falkcorp/mediacore/config"
)

// These cells run the real config-load path (OperatorAppConfig: core's operator
// layers, then the same projection the booted service builds) and serve's path
// resolution (ResolveBootPaths), with the old setting and the new one set by file
// key and by canonical env name. The canonical env names are the F3-07 prod
// census's spellings, pinned as literals.

const (
	oldOpenAIEnv = "ABO_INTEGRATIONS_AI_OPENAI_BASE_URL"
	newOpenAIEnv = "ABO_INTEGRATIONS_AI_OPENAI_URL"

	// sentinels are values no message may contain: a recognisable host per setting.
	sentinelOld = "https://sentinel-old-1b7e.invalid/v1"
	sentinelNew = "https://sentinel-new-9c4d.invalid/v1"
	sentinelKey = "sentinel-credential-5e02"
)

// setting is one operator-layer value: an env variable, or a key of the operator
// file given by its dotted path (a nil value is a YAML null).
type setting struct {
	env  map[string]string
	file map[string]any
}

// boot runs OperatorAppConfig over the settings and returns the error and the
// projected openai URL.
func bootWith(t *testing.T, sets ...setting) (string, error) {
	t.Helper()
	operatorEnv(t)
	boot := BootInputs{}
	doc := map[string]any{}
	for _, s := range sets {
		for k, v := range s.env {
			t.Setenv(k, v)
		}
		for path, v := range s.file {
			nest(doc, strings.Split(path, "."), v)
		}
	}
	if len(doc) > 0 {
		raw, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		boot.ConfigFile = filepath.Join(t.TempDir(), "gotham.yaml")
		if err := os.WriteFile(boot.ConfigFile, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := OperatorAppConfig(t.Context(), Module(), boot)
	return cfg.OpenAIBaseURL, err
}

func envSet(name, v string) setting { return setting{env: map[string]string{name: v}} }

func fileSet(path string, v any) setting { return setting{file: map[string]any{path: v}} }

// nest puts v at the dotted path of doc, making the maps on the way.
func nest(doc map[string]any, segs []string, v any) {
	if len(segs) == 1 {
		doc[segs[0]] = v
		return
	}
	next, _ := doc[segs[0]].(map[string]any)
	if next == nil {
		next = map[string]any{}
		doc[segs[0]] = next
	}
	nest(next, segs[1:], v)
}

func oldOpenAIFile(v string) setting { return fileSet("integrations.ai.openai.base_url", v) }

func newOpenAIFile(v string) setting { return fileSet("integrations.ai.openai.url", v) }

// requireRefused fails unless err refuses the boot as a retired operator setting,
// names the key and the setting to use, and holds none of the values.
func requireRefused(t *testing.T, err error, mustName ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("the boot was accepted; want it refused")
	}
	if !errors.Is(err, coreconfig.ErrRetiredName) {
		t.Errorf("error %v does not wrap ErrRetiredName", err)
	}
	for _, want := range mustName {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	for _, v := range []string{sentinelOld, sentinelNew, sentinelKey, "sentinel-"} {
		if strings.Contains(err.Error(), v) {
			t.Errorf("error %q prints a value (%q)", err, v)
		}
	}
}

func TestRetiredOperatorSettings_OpenAIBaseURLCells(t *testing.T) {
	oldSpellings := map[string]func(string) setting{
		"old by file key": oldOpenAIFile,
		"old by env":      func(v string) setting { return envSet(oldOpenAIEnv, v) },
	}
	newSpellings := map[string]func(string) setting{
		"new by file key": newOpenAIFile,
		"new by env":      func(v string) setting { return envSet(newOpenAIEnv, v) },
	}
	for on, old := range oldSpellings {
		// Only the old path set: refused, naming the new path.
		t.Run(on+" alone", func(t *testing.T) {
			_, err := bootWith(t, old(sentinelOld))
			requireRefused(t, err, "integrations.ai.openai.url", "base_url")
		})
		for nn, nw := range newSpellings {
			// Both set and equal: still refused, the old one is to be removed.
			t.Run(on+" and "+nn+" equal", func(t *testing.T) {
				_, err := bootWith(t, old(sentinelNew), nw(sentinelNew))
				requireRefused(t, err, "integrations.ai.openai.url")
			})
			// Both set and different: refused.
			t.Run(on+" and "+nn+" different", func(t *testing.T) {
				_, err := bootWith(t, old(sentinelOld), nw(sentinelNew))
				requireRefused(t, err, "integrations.ai.openai.url")
			})
		}
	}
	for nn, nw := range newSpellings {
		// Only the new path set: accepted, and it is the effective URL.
		t.Run(nn+" alone", func(t *testing.T) {
			got, err := bootWith(t, nw(sentinelNew))
			if err != nil {
				t.Fatalf("the new path was refused: %v", err)
			}
			if got != sentinelNew {
				t.Errorf("effective openai URL = %q, want the value set at the new path", got)
			}
		})
	}
	// OPENAI_BASE_URL, the ecosystem name, binds to the new path.
	t.Run("OPENAI_BASE_URL", func(t *testing.T) {
		got, err := bootWith(t, envSet("OPENAI_BASE_URL", sentinelNew))
		if err != nil || got != sentinelNew {
			t.Errorf("OPENAI_BASE_URL gave %q, %v; want %q and no error", got, err, sentinelNew)
		}
	})
	// Neither set: accepted.
	t.Run("neither", func(t *testing.T) {
		if _, err := bootWith(t); err != nil {
			t.Fatalf("a boot with neither set was refused: %v", err)
		}
	})
	// An unset (empty) old env supplies nothing, as core reads it.
	t.Run("old env empty", func(t *testing.T) {
		if _, err := bootWith(t, envSet(oldOpenAIEnv, "")); err != nil {
			t.Fatalf("an empty old env was refused: %v", err)
		}
	})
	// A null old file key supplies nothing either.
	t.Run("old file key null", func(t *testing.T) {
		if _, err := bootWith(t, fileSet("integrations.ai.openai.base_url", nil)); err != nil {
			t.Fatalf("a null old file key was refused: %v", err)
		}
	})
}

func TestRetiredOperatorSettings_MetadataCredentialsFamily(t *testing.T) {
	refused := map[string]setting{
		"google-books by env":      envSet("ABO_INTEGRATIONS_METADATA_GOOGLE_BOOKS_CREDENTIALS_API_KEY", sentinelKey),
		"hardcover by env":         envSet("ABO_INTEGRATIONS_METADATA_HARDCOVER_CREDENTIALS_API_TOKEN", sentinelKey),
		"a renamed source by env":  envSet("ABO_INTEGRATIONS_METADATA_BATGIRL_CREDENTIALS_API_KEY", sentinelKey),
		"google-books by file key": fileSet("integrations.metadata.google-books.credentials.api_key", sentinelKey),
		"hardcover by file key":    fileSet("integrations.metadata.hardcover.credentials.api_token", sentinelKey),
		"any source, any key":      fileSet("integrations.metadata.openlibrary.credentials.apiKey", sentinelKey),
	}
	for name, s := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := bootWith(t, s)
			requireRefused(t, err, "stored secret", "Settings")
			if strings.Contains(err.Error(), "unknown key") || strings.Contains(err.Error(), "duplicate path") {
				t.Errorf("error %q is core's generic message, want the books refusal", err)
			}
		})
	}
	// The 4-segment declared secrets stay accepted: they are the adapters' fields.
	for name, s := range map[string]setting{
		"hardcover api_token by env":   envSet("ABO_INTEGRATIONS_METADATA_HARDCOVER_API_TOKEN", sentinelKey),
		"google-books api_key by file": fileSet("integrations.metadata.google-books.api_key", sentinelKey),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := bootWith(t, s); err != nil {
				t.Fatalf("a declared passthrough secret was refused: %v", err)
			}
		})
	}
}

// valueFile writes v to a file and returns its path: what a <NAME>_FILE variable
// names.
func valueFile(t *testing.T, v string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRetiredOperatorSettings_FileSpelling: core reads any env name as
// <NAME>_FILE=<path> (core/config/env.go), and on main the retired openai name
// given that way was projected into the OpenAI base URL. It must be refused like
// the plain spelling, for the openai path and for the credentials family, naming
// the variable as set and never the path's contents. Mutant: EnvForms without
// the _FILE form (the round-1 F1 finding).
func TestRetiredOperatorSettings_FileSpelling(t *testing.T) {
	for _, name := range []string{
		oldOpenAIEnv,
		"ABO_INTEGRATIONS_METADATA_HARDCOVER_CREDENTIALS_API_TOKEN",
		"ABO_INTEGRATIONS_METADATA_GOOGLE_BOOKS_CREDENTIALS_API_KEY",
		"ABO_INTEGRATIONS_METADATA_BATGIRL_CREDENTIALS_API_KEY",
	} {
		t.Run(name+"_FILE", func(t *testing.T) {
			_, err := bootWith(t, envSet(name+"_FILE", valueFile(t, sentinelKey)))
			requireRefused(t, err, name+"_FILE")
		})
	}
	t.Run("the new openai name as a file is accepted and effective", func(t *testing.T) {
		got, err := bootWith(t, envSet(newOpenAIEnv+"_FILE", valueFile(t, sentinelNew)))
		if err != nil || got != sentinelNew {
			t.Errorf("%s_FILE gave %q, %v; want %q and no error", newOpenAIEnv, got, err, sentinelNew)
		}
	})
	t.Run("an empty _FILE variable is unset", func(t *testing.T) {
		if _, err := bootWith(t, envSet(oldOpenAIEnv+"_FILE", "")); err != nil {
			t.Fatalf("an empty %s_FILE was refused: %v", oldOpenAIEnv, err)
		}
	})
}

// TestRetiredOperatorSettings_NullIsUnsetOnlyForTheDirectSpelling: core reads a
// literal null as unset (05-config section 1.3.5), so NAME=null supplies nothing
// and is not refused (mutant: drop the null exemption). For NAME_FILE the value
// is a path and the literal is tested against the file's contents, so
// NAME_FILE=null is a file core tries to read and is refused (mutant: apply the
// exemption to _FILE too). Python's deploy check reads no value, so it refuses a
// listed name whatever it is set to: it errs toward refusing.
func TestRetiredOperatorSettings_NullIsUnsetOnlyForTheDirectSpelling(t *testing.T) {
	for _, name := range []string{oldOpenAIEnv, "ABO_INTEGRATIONS_METADATA_HARDCOVER_CREDENTIALS_API_TOKEN"} {
		t.Run(name+"=null", func(t *testing.T) {
			if _, err := bootWith(t, envSet(name, "null")); err != nil {
				t.Fatalf("%s=null was refused: %v", name, err)
			}
		})
		t.Run(name+"_FILE=null", func(t *testing.T) {
			_, err := bootWith(t, envSet(name+"_FILE", "null"))
			requireRefused(t, err, name+"_FILE")
		})
	}
}

// TestRetiredOperatorSettings_AnEnvUserIsToldTheEnvSpelling (N4): a host that set
// the old openai name by env is told the env spelling of the replacement as well as
// the path, at boot (both entry points name it).
func TestRetiredOperatorSettings_AnEnvUserIsToldTheEnvSpelling(t *testing.T) {
	_, err := bootWith(t, envSet(oldOpenAIEnv, sentinelOld))
	requireRefused(t, err, newOpenAIEnv, "integrations.ai.openai.url")
	operatorEnv(t)
	t.Setenv(oldOpenAIEnv, sentinelOld)
	_, _, err = ResolveBootPaths(t.Context(), Module(), BootInputs{})
	requireRefused(t, err, newOpenAIEnv, "integrations.ai.openai.url")
}

// TestRetiredOperatorSettings_ServePathRefuses: serve resolves its paths first
// (ResolveBootPaths), so a host that still sets an old name never gets as far as
// opening a store.
func TestRetiredOperatorSettings_ServePathRefuses(t *testing.T) {
	operatorEnv(t)
	t.Setenv(oldOpenAIEnv, sentinelOld)
	_, _, err := ResolveBootPaths(t.Context(), Module(), BootInputs{})
	requireRefused(t, err, oldOpenAIEnv, "integrations.ai.openai.url")
}

// TestRetiredOperatorSettings_TheMessageNamesKeysOnly drives the check with
// environments directly: every refusal names the variable or the file path and
// never a value, even for an unusual value.
func TestRetiredOperatorSettings_TheMessageNamesKeysOnly(t *testing.T) {
	err := refuseRetiredOperatorSettings("", []string{
		"PATH=/usr/bin",
		oldOpenAIEnv + "=" + sentinelOld,
		"ABO_INTEGRATIONS_METADATA_HARDCOVER_CREDENTIALS_API_TOKEN=" + sentinelKey,
		"ABO_INTEGRATIONS_METADATA_GOOGLE_BOOKS_CREDENTIALS_API_KEY=",
	})
	requireRefused(t, err, oldOpenAIEnv, "ABO_INTEGRATIONS_METADATA_HARDCOVER_CREDENTIALS_API_TOKEN")
	if strings.Contains(err.Error(), "GOOGLE_BOOKS") {
		t.Errorf("an empty env was named: %v", err)
	}
	if err := refuseRetiredOperatorSettings("", []string{"ABO_INTEGRATIONS_AI_OPENAI_URL=" + sentinelNew}); err != nil {
		t.Errorf("the new name was refused: %v", err)
	}
}
