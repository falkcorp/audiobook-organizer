// file: internal/books/flip_no_plaintext_test.go
// version: 1.0.0
// guid: 6b2d90e4-1a73-4c85-9f0e-d4a7358c1b62
// last-edited: 2026-11-20

package books

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// canaryValue is the plaintext planted for one form: 30 characters of base64 of a
// hash of the form's name. Pebble compresses table blocks with back-references to
// repeated runs, so a canary with a prefix shared by the others is not contiguous
// in a table and a byte scan misses it; a high-entropy value is.
func canaryValue(form string) string {
	sum := sha256.Sum256([]byte("gotham/" + form))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:30]
}

// canarySecret is one plaintext credential planted in one of HEAD's stored forms.
type canarySecret struct {
	form  string // where HEAD kept it
	value string
}

// canaryBlob is the settings blob HEAD wrote with its secrets in plaintext: the
// forms the cutover moves into core's secrets store (the encrypted rows are the
// other form, canaryRows).
func canaryBlob() (blob string, planted []canarySecret) {
	planted = []canarySecret{
		{"blob acoustid_api_key", canaryValue("blob acoustid_api_key")},
		{"blob deluge_web_password", canaryValue("blob deluge_web_password")},
		{"blob download_client deluge password", canaryValue("blob download_client deluge password")},
		{"blob oauth_github_client_secret", canaryValue("blob oauth_github_client_secret")},
		{"blob oauth_google_client_secret", canaryValue("blob oauth_google_client_secret")},
		{"blob plugins webhook secret", canaryValue("blob plugins webhook secret")},
		{"blob plugins batsignal token", canaryValue("blob plugins batsignal token")},
		{"blob metadata source credentials", canaryValue("blob metadata source credentials")},
	}
	blob = fmt.Sprintf(`{"concurrent_scans":2,"embedding_enabled":true,"embedding_model":"batcomputer",`+
		`"acoustid_api_key":%q,"deluge_web_password":%q,`+
		`"download_client":{"torrent":{"deluge":{"password":%q}}},`+
		`"oauth_enabled":true,"oauth_github_client_id":"robin-github","oauth_github_client_secret":%q,`+
		`"oauth_google_client_id":"batgirl-google","oauth_google_client_secret":%q,`+
		`"plugins":{"webhook":{"enabled":true,"settings":{"events":"all","urls":"http://192.0.2.10/gotham","secret":%q}},`+
		`"batsignal":{"enabled":true,"settings":{"token":%q}}},`+
		`"metadata_sources":[{"id":"hardcover","display_name":"Hardcover","enabled":true,"priority":1,"credentials":{"apiKey":%q}}]}`,
		planted[0].value, planted[1].value, planted[2].value, planted[3].value, planted[4].value,
		planted[5].value, planted[6].value, planted[7].value)
	return blob, planted
}

// canaryRows are the four encrypted setting rows HEAD held (the key that seals
// them is the state directory's), by row name. The three that config.yaml can also
// carry (HEAD's gap-fill) are mirrored there with the same value.
func canaryRows() map[string]canarySecret {
	return map[string]canarySecret{
		"openai_api_key":       {"row openai_api_key", canaryValue("row openai_api_key")},
		"google_books_api_key": {"row google_books_api_key", canaryValue("row google_books_api_key")},
		"hardcover_api_token":  {"row hardcover_api_token", canaryValue("row hardcover_api_token")},
		"basic_auth_password":  {"row basic_auth_password", canaryValue("row basic_auth_password")},
	}
}

// filesHolding returns, sorted, the files under each root that hold needle,
// skipping any path under an excluded directory. A scan reads every file whole:
// SSTs, WAL and recycled logs, MANIFEST and OPTIONS are all files of the store
// directory.
func filesHolding(t *testing.T, needle string, exclude []string, roots ...string) []string {
	t.Helper()
	var hits []string
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if slices.Contains(exclude, p) {
					return filepath.SkipDir
				}
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if bytes.Contains(b, []byte(needle)) {
				hits = append(hits, p)
			}
			return nil
		}))
	}
	sort.Strings(hits)
	return hits
}

// flipFixture is a HEAD store on disk (a settings blob, encrypted secret rows and
// an optional config.yaml) with the state directory that seals it, ready for the
// flipped composition.
type flipFixture struct {
	root, stateDir, dbDir, dbPath, gapFill string
	key                                    []byte
}

// newFlipFixture writes the HEAD store through seed (which writes the settings
// rows) and yaml (when non-empty) as config.yaml beside the store.
func newFlipFixture(t *testing.T, seed func(*database.PebbleStore), yaml string) *flipFixture {
	t.Helper()
	root := tempDir(t)
	fx := &flipFixture{
		root: root, stateDir: filepath.Join(root, "state"), dbDir: filepath.Join(root, "db"),
		key: bytes.Repeat([]byte{7}, 32),
	}
	fx.dbPath = filepath.Join(fx.dbDir, "books.pebble")
	fx.gapFill = filepath.Join(fx.dbDir, "config.yaml")
	require.NoError(t, os.MkdirAll(fx.stateDir, 0o700))
	require.NoError(t, os.MkdirAll(fx.dbDir, 0o700))
	require.NoError(t, os.WriteFile(database.EncryptionKeyPath(fx.stateDir), fx.key, 0o600))
	aboFlipFixtureMu.Lock()
	require.NoError(t, database.InitEncryption(fx.stateDir))
	headStoreAt(t, fx.dbPath, seed)
	aboFlipFixtureMu.Unlock()
	if yaml != "" {
		require.NoError(t, os.WriteFile(fx.gapFill, []byte(yaml), 0o600))
	}
	return fx
}

// runFlipped boots the flipped composition on the fixture, waits for ready, calls
// atReady (when set) with the booted composition, and stops it cleanly. Run, not
// only boot: the gap-fill strip is a repairs-hook step, which runs at start.
func (fx *flipFixture) runFlipped(t *testing.T, atReady func(*composed)) {
	t.Helper()
	old := flipDeclared
	flipDeclared = true
	t.Cleanup(func() { flipDeclared = old })
	r := runComposedIn(t, fx.stateDir, func(cfg *config.Config) {
		cfg.DatabasePath = fx.dbPath
		cfg.RootDir = fx.dbDir
	}, nil, func(o *app.Options) {
		o.StorageFS = storage.FS{}
		o.StateDir = fx.stateDir
		o.Env = []string{storagePathEnv + "=" + fx.dbPath}
		// sandboxStateDirAt wrote a fixed key; the rows are sealed under this one.
		require.NoError(t, os.WriteFile(database.EncryptionKeyPath(fx.stateDir), fx.key, 0o600))
	}, nil)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ready, err := shapedReady(r.composed)
		require.NoError(t, err)
		if ready {
			break
		}
		require.True(t, time.Now().Before(deadline), "/health never reported ready")
		time.Sleep(50 * time.Millisecond)
	}
	if atReady != nil {
		atReady(r.composed)
	}
	require.NoError(t, r.stop())
}

// TestConfigCutover_NoPlaintextSecretOnDisk (05-config 1.12.2; 05-storage 5S.3.13):
// a canary secret planted in every HEAD form before the upgrade (the encrypted
// rows, the blob's plaintext secrets, config.yaml's gap-fill keys) appears in no
// file under the live DB directory, the state directory or the gap-fill file
// after the whole cutover (M2, M6 and the forced compaction M8) and a clean
// close. The retained checkpoint is the one exception, until the sweeper deletes
// it; the test requires it to hold the canaries, which also proves the fixture
// planted each form and the scan can see it.
func TestConfigCutover_NoPlaintextSecretOnDisk(t *testing.T) {
	blob, planted := canaryBlob()
	rows := canaryRows()
	secretRows := map[string]string{}
	for name, r := range rows {
		planted = append(planted, r)
		secretRows[name] = r.value
	}
	yaml := fmt.Sprintf("openai_api_key: %s\ngoogle_books_api_key: %s\nhardcover_api_token: %s\n",
		rows["openai_api_key"].value, rows["google_books_api_key"].value, rows["hardcover_api_token"].value)
	fx := newFlipFixture(t, func(s *database.PebbleStore) {
		require.NoError(t, s.SetSetting("config_blob", blob, "json", false))
		for name, v := range secretRows {
			require.NoError(t, s.SetSetting(name, v, "string", true))
		}
	}, yaml)
	fx.runFlipped(t, nil)
	root, stateDir, dbDir, dbPath, gapFill := fx.root, fx.stateDir, fx.dbDir, fx.dbPath, fx.gapFill

	checkpoints := filepath.Join(dbDir, ".mediacore-checkpoints")
	require.DirExists(t, checkpoints, "the cutover boot took no checkpoint")
	entries, err := os.ReadDir(checkpoints)
	require.NoError(t, err)
	var cpDB string
	for _, e := range entries {
		if d := filepath.Join(checkpoints, e.Name(), "db"); e.IsDir() && e.Name() != "scratch" && !strings.HasSuffix(e.Name(), ".tmp") {
			if _, err := os.Stat(d); err == nil {
				cpDB = d
			}
		}
	}
	require.NotEmpty(t, cpDB, "no checkpoint holds a store copy")

	// Pebble compresses table blocks, so a byte scan of an SST can miss a plaintext
	// value: the decoded rows are scanned too, live and in the checkpoint.
	liveRows, cpRows := rawKVs(t, dbPath), rawKVs(t, cpDB)
	holds := func(kvs map[string]string, needle string) bool {
		for _, v := range kvs {
			if strings.Contains(v, needle) {
				return true
			}
		}
		return false
	}
	for _, s := range planted {
		live := filesHolding(t, s.value, []string{checkpoints}, dbDir, stateDir)
		require.Empty(t, relTo(root, live), "%s: the canary is on disk in plaintext after the cutover", s.form)
		require.False(t, holds(liveRows, s.value), "%s: a live store row holds the canary in plaintext", s.form)
		// The checkpoint is where the old bytes stay, until the sweeper removes it.
		// Rows are sealed in HEAD's store, so only the plaintext forms can be seen
		// there: the blob's.
		if strings.HasPrefix(s.form, "blob ") {
			require.True(t, holds(cpRows, s.value), "%s: the checkpoint does not hold the canary: the fixture planted nothing", s.form)
		}
	}
	require.NotEmpty(t, filesHolding(t, rows["openai_api_key"].value, nil, checkpoints),
		"config.yaml's gap-fill canary is not in the checkpoint's state copy")
	// The gap-fill file itself: stripped (its keys' values live in core's secrets store).
	b, err := os.ReadFile(gapFill)
	if !os.IsNotExist(err) {
		require.NoError(t, err)
		for _, r := range rows {
			require.NotContains(t, string(b), r.value, "config.yaml still holds %s", r.form)
		}
	}
}

// relTo is the paths relative to root, for a failure message that stays short.
func relTo(root string, paths []string) []string {
	var out []string
	for _, p := range paths {
		r, err := filepath.Rel(root, p)
		if err != nil {
			r = p
		}
		out = append(out, r)
	}
	return out
}
