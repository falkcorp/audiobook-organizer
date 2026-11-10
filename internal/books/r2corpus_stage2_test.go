// file: internal/books/r2corpus_stage2_test.go
// version: 1.0.0
// guid: 27e316b7-79f4-4da7-9d37-2321e45eb255
// last-edited: 2026-11-10

package books

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The stage-2 corpus (F2-13) read back from the committed stores: every key
// the manifest names is in its store, every deliberately broken value is
// broken, and the blocker stores hold exactly the blocking rows the manifest
// says. The stores are opened read-only from a copy; nothing here boots R0 or
// any store code, so the test reads the bytes a cutover test will read.

type r2Stage2Manifest struct {
	Overlay  []r2Stage2Case `json:"overlay"`
	Blockers []r2Stage2Case `json:"blockers"`
	Stores   map[string]any `json:"stores"`
}

type r2Stage2Case struct {
	Label       string            `json:"label"`
	Keys        []string          `json:"keys"`
	RemovedKeys []string          `json:"removed_keys"`
	CorruptKeys []string          `json:"corrupt_keys"`
	Outcome     string            `json:"outcome"`
	Store       string            `json:"store"`
	ImportedAs  map[string]string `json:"imported_as"`
}

func loadR2Stage2Manifest(t *testing.T) r2Stage2Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r2corpusDir, "manifest.json"))
	require.NoError(t, err)
	var m r2Stage2Manifest
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// r2ReadStore copies a corpus store's Pebble directory and reads every key of
// the copy, opened read-only.
func r2ReadStore(t *testing.T, store string) map[string][]byte {
	t.Helper()
	src := filepath.Join(r2RepoRoot(t), filepath.FromSlash(store), "db", "audiobooks.pebble")
	dst := filepath.Join(t.TempDir(), "audiobooks.pebble")
	require.NoError(t, filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	}))
	db, err := pebble.Open(dst, &pebble.Options{ReadOnly: true})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	it, err := db.NewIter(nil)
	require.NoError(t, err)
	rows := map[string][]byte{}
	for it.First(); it.Valid(); it.Next() {
		rows[string(it.Key())] = append([]byte(nil), it.Value()...)
	}
	require.NoError(t, it.Close())
	require.NotEmpty(t, rows, store)
	return rows
}

// r2Blocking is every condition the cutover preflight refuses (09 §9.2.3;
// 05-storage §5S.3.13 TestCutoverStopsOnUnconvertibleIdentity,
// TestCutoverRefusedWhileMergeInProgress), as "<reason> <key>": an identity
// row that does not decode, a user row auth.Importer rejects (no username),
// an API key or OAuth identity whose user does not exist, and a merge journal
// at applying or undoing.
func r2Blocking(rows map[string][]byte) []string {
	users := map[string]bool{}
	for k := range rows {
		if strings.HasPrefix(k, "u:") {
			users[strings.TrimPrefix(k, "u:")] = true
		}
	}
	var out []string
	for k, v := range rows {
		switch {
		case strings.HasPrefix(k, "u:"):
			var u struct {
				Username string `json:"username"`
			}
			if json.Unmarshal(v, &u) != nil {
				out = append(out, "undecodable "+k)
			} else if strings.TrimSpace(u.Username) == "" {
				out = append(out, "rejected "+k)
			}
		case strings.HasPrefix(k, "apikey:"), strings.HasPrefix(k, "oauthid:"):
			var r struct {
				UserID string `json:"user_id"`
			}
			if json.Unmarshal(v, &r) != nil {
				out = append(out, "undecodable "+k)
			} else if !users[r.UserID] {
				out = append(out, "orphan "+k)
			}
		case strings.HasPrefix(k, "usermerge:journal:"):
			var j struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(v, &j) != nil {
				out = append(out, "undecodable "+k)
			} else if j.Status == "applying" || j.Status == "undoing" {
				out = append(out, "merge-"+j.Status+" "+k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// r2Undecryptable reports whether a secret setting record's value fails to
// open under the store's key (HEAD's settings.go: AES-256-GCM, value =
// base64(nonce || sealed), key = state/.encryption_key).
func r2Undecryptable(t *testing.T, store string, record []byte) bool {
	t.Helper()
	var s struct {
		Value    string `json:"value"`
		IsSecret bool   `json:"is_secret"`
	}
	require.NoError(t, json.Unmarshal(record, &s))
	require.True(t, s.IsSecret, "a corrupt setting that decodes must be a secret")
	key, err := os.ReadFile(filepath.Join(r2RepoRoot(t), filepath.FromSlash(store), "state", ".encryption_key"))
	require.NoError(t, err)
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(s.Value)
	if err != nil || len(raw) < gcm.NonceSize() {
		return true
	}
	_, err = gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return err != nil
}

func TestR2CorpusStage2_StoresHoldTheirCases(t *testing.T) {
	m := loadR2Stage2Manifest(t)
	require.NotEmpty(t, m.Overlay)
	require.NotEmpty(t, m.Blockers)

	byStore := map[string][]r2Stage2Case{}
	for _, c := range append(append([]r2Stage2Case(nil), m.Overlay...), m.Blockers...) {
		byStore[c.Store] = append(byStore[c.Store], c)
	}
	stage1 := r2corpusDirRel + "/stage1"
	byStore[stage1] = nil // the base: no case, no blocking row

	for store, cases := range byStore {
		t.Run(filepath.Base(store), func(t *testing.T) {
			_, listed := m.Stores[store]
			require.True(t, listed, "%s is not in the manifest's stores", store)
			rows := r2ReadStore(t, store)

			for _, c := range cases {
				require.NotEmpty(t, c.Keys, c.Label)
				for _, k := range c.Keys {
					row, _, _ := strings.Cut(k, "#")
					_, ok := rows[row]
					assert.Truef(t, ok, "%s: key %s is not in %s", c.Label, k, store)
				}
				for _, k := range c.RemovedKeys {
					_, ok := rows[k]
					assert.Falsef(t, ok, "%s: removed key %s is in %s", c.Label, k, store)
				}
				corruptCase := strings.HasPrefix(c.Label, "corrupt-") || strings.HasPrefix(c.Label, "blocker-identity-corrupt-")
				if !corruptCase {
					assert.Empty(t, c.CorruptKeys, "%s writes no corrupt value", c.Label)
					continue
				}
				require.NotEmpty(t, c.CorruptKeys, "%s must name its corrupt key", c.Label)
				for _, k := range c.CorruptKeys {
					assert.Contains(t, c.Keys, k, c.Label)
					v, ok := rows[k]
					require.True(t, ok, "%s: corrupt key %s missing", c.Label, k)
					var probe json.RawMessage
					if json.Unmarshal(v, &probe) == nil {
						assert.True(t, r2Undecryptable(t, store, v), "%s: %s decodes and decrypts", c.Label, k)
					}
				}
			}

			blocking := r2Blocking(rows)
			var refused, terminal []r2Stage2Case
			for _, c := range cases {
				switch c.Outcome {
				case "refused":
					refused = append(refused, c)
				case "not_blocking":
					terminal = append(terminal, c)
				}
			}
			switch {
			case len(refused) > 0:
				require.Len(t, refused, 1, "%s: one refused case per store", store)
				require.Len(t, blocking, 1, "%s: exactly one blocking condition, got %v", store, blocking)
				_, key, _ := strings.Cut(blocking[0], " ")
				assert.Contains(t, refused[0].Keys, key, "%s: the blocking row is not the case's", refused[0].Label)
			default:
				assert.Empty(t, blocking, "%s must not block (%d control cases)", store, len(terminal))
			}
		})
	}
}

// r2corpusDirRel is r2corpusDir as the manifest names it (repo-relative).
const r2corpusDirRel = "internal/books/" + r2corpusDir
