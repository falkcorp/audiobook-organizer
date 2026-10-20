// file: internal/database/settings_getall_test.go
// version: 1.1.1
// guid: 3f0d6a52-9c1e-4b7a-8e25-6d4c1f0a9b83
// last-edited: 2026-10-19

package database

import (
	"errors"
	"strings"
	"testing"
)

// rawSettingRow writes bytes under a settings key directly, bypassing SetSetting's
// JSON envelope, to simulate a corrupt row.
func rawSettingRow(t *testing.T, store Store, key, value string) {
	t.Helper()
	ps, ok := store.(*PebbleStore)
	if !ok {
		t.Fatalf("store is %T, want *PebbleStore", store)
	}
	if err := ps.db.Set([]byte(settingKeyPrefix+key), []byte(value), nil); err != nil {
		t.Fatalf("raw set %q: %v", key, err)
	}
}

func TestGetAllSettings_ReturnsAllValidRows(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	for _, k := range []string{"a", "b", "config_blob"} {
		if err := store.SetSetting(k, "v-"+k, "string", false); err != nil {
			t.Fatalf("SetSetting %q: %v", k, err)
		}
	}
	got, err := store.GetAllSettings()
	if err != nil {
		t.Fatalf("GetAllSettings: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(got), got)
	}
}

func TestGetAllSettings_EmptyKeyspaceIsNotAnError(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	got, err := store.GetAllSettings()
	if err != nil {
		t.Fatalf("GetAllSettings on empty store: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows from an empty store", len(got))
	}
}

// A corrupt config_blob row used to be skipped, so LoadConfigFromDatabase saw
// "no blob", ran on defaults, and the next SaveConfigToDatabase overwrote it.
func TestGetAllSettings_CorruptConfigBlobFails(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	if err := store.SetSetting("good", "1", "int", false); err != nil {
		t.Fatal(err)
	}
	rawSettingRow(t, store, "config_blob", `{not json SECRETVALUE`)

	got, err := store.GetAllSettings()
	if err == nil {
		t.Fatalf("GetAllSettings returned nil error and %d rows for a corrupt config_blob row", len(got))
	}
	if got != nil {
		t.Fatalf("partial result returned alongside the error: %+v", got)
	}
	if !strings.Contains(err.Error(), `"config_blob"`) {
		t.Fatalf("error does not name the corrupt key: %v", err)
	}
	if strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("error echoes the stored bytes: %v", err)
	}
}

// Any corrupt row fails, not only config_blob: a dropped secret row is blanked
// by the next save and hides an encrypted secret from the key-regeneration
// guard. Every bad key is named at once.
func TestGetAllSettings_EveryCorruptRowIsNamed(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	rawSettingRow(t, store, "openai_api_key", `garbage`)
	rawSettingRow(t, store, "some_flag", ``)

	_, err := store.GetAllSettings()
	if err == nil {
		t.Fatal("GetAllSettings returned nil error for corrupt rows")
	}
	for _, k := range []string{`"openai_api_key"`, `"some_flag"`} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error does not name %s: %v", k, err)
		}
	}
	var unreadable *UnreadableSettingsError
	if !errors.As(err, &unreadable) {
		t.Fatalf("err is %T, want *UnreadableSettingsError: %v", err, err)
	}
	if got := strings.Join(unreadable.Keys, ","); got != "openai_api_key,some_flag" {
		t.Errorf("Keys = %q, want openai_api_key,some_flag (no prefix, key order)", got)
	}
}

// fakeIter replays rows and then reports iterErr / closeErr.
type fakeIter struct {
	keys, vals []string
	i          int
	iterErr    error
	closeErr   error
	closed     bool
}

func (f *fakeIter) First() bool   { f.i = 0; return f.Valid() }
func (f *fakeIter) Valid() bool   { return f.i < len(f.keys) }
func (f *fakeIter) Next() bool    { f.i++; return f.Valid() }
func (f *fakeIter) Key() []byte   { return []byte(f.keys[f.i]) }
func (f *fakeIter) Value() []byte { return []byte(f.vals[f.i]) }
func (f *fakeIter) Error() error  { return f.iterErr }
func (f *fakeIter) Close() error  { f.closed = true; return f.closeErr }

const validRow = `{"key":"config_blob","value":"{}","type":"json","is_secret":false}`

// An I/O or corruption error mid-iteration ends the loop with Valid()==false;
// without the iter.Error() check the rows read so far came back as the whole
// list with a nil error.
func TestCollectSettings_IteratorErrorFails(t *testing.T) {
	ioErr := errors.New("simulated pebble read error")
	it := &fakeIter{keys: []string{"setting:config_blob"}, vals: []string{validRow}, iterErr: ioErr}
	got, err := collectSettings(it)
	if !errors.Is(err, ioErr) {
		t.Fatalf("err = %v, want wrapping %v", err, ioErr)
	}
	// Not reported as unreadable rows: the census itself failed, so the
	// caller's "repair the named rows" remedy would be wrong.
	if _, ok := errors.AsType[*UnreadableSettingsError](err); ok {
		t.Fatalf("iterator error reported as unreadable rows: %v", err)
	}
	if got != nil {
		t.Fatalf("partial result returned alongside the error: %+v", got)
	}
	if !it.closed {
		t.Fatal("iterator not closed")
	}
}

func TestCollectSettings_CloseErrorFails(t *testing.T) {
	closeErr := errors.New("simulated close error")
	it := &fakeIter{keys: []string{"setting:config_blob"}, vals: []string{validRow}, closeErr: closeErr}
	got, err := collectSettings(it)
	if !errors.Is(err, closeErr) {
		t.Fatalf("err = %v, want wrapping %v", err, closeErr)
	}
	if got != nil {
		t.Fatalf("result returned alongside the close error: %+v", got)
	}
}

func TestCollectSettings_CleanIteration(t *testing.T) {
	it := &fakeIter{keys: []string{"setting:config_blob"}, vals: []string{validRow}}
	got, err := collectSettings(it)
	if err != nil {
		t.Fatalf("collectSettings: %v", err)
	}
	if len(got) != 1 || got[0].Key != "config_blob" {
		t.Fatalf("got %+v", got)
	}
}

// A corrupt row seen before an iterator error must not turn the I/O failure
// into an "unreadable rows" error: the key list may be incomplete.
func TestCollectSettings_IteratorErrorWinsOverUnreadableRow(t *testing.T) {
	ioErr := errors.New("simulated pebble read error")
	it := &fakeIter{keys: []string{"setting:bad"}, vals: []string{"garbage"}, iterErr: ioErr}
	_, err := collectSettings(it)
	var unreadable *UnreadableSettingsError
	if !errors.Is(err, ioErr) || errors.As(err, &unreadable) {
		t.Fatalf("err = %v (%T), want the wrapped iterator error only", err, err)
	}
}
