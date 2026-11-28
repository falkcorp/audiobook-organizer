// file: cmd/diagnostics_user_state_test.go
// version: 1.0.0
// guid: 8d13b44e-4f42-4be1-82f6-59d19ed0c4cd
// last-edited: 2026-11-28

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The F3-07 probe, `diagnostics user-state` (F4-08 fix-forward sweep).

const (
	usListedProgressBook  = "book-batcave-orphan-7f3a"     // a listed user's real progress; no book: row
	usListedBlankBook     = "book-arkham-blank-2c91"       // a listed user's empty state row: progress, not carryable
	usUnlistedOnlyBook    = "book-ace-chemicals-unlisted"  // state only from a user the list does not hold
	usUnlistedUser        = "user-red-hood-not-in-core"    // a user id core does not hold
	usSampleMissingBook   = "book-wayne-manor-never-heard" // asked in --books, has nothing
	usSecretKeyInStrictEr = "u:joker-secret-row-key"       // the key text a strict error names
)

// userStateFixture is a started (migrated) store with state rows: two books of
// core's system user (listed: it is core's _system account), one of them with
// carryable progress, and one book whose only state is an unlisted user's.
func userStateFixture(t *testing.T) string {
	t.Helper()
	isolateCLIStore(t)
	restoreRootFlags(t)
	dbPath := os.Getenv("ABO_CORE_STORAGE_PATH")
	s, err := startedStore(t, dbPath)
	if err != nil {
		t.Fatalf("open the started store: %v", err)
	}
	for _, st := range []*database.UserBookState{
		{UserID: "_system", BookID: usListedProgressBook, Status: "in_progress", ProgressPct: 41, TotalListenedSeconds: 600},
		{UserID: "_system", BookID: usListedBlankBook},
		{UserID: usUnlistedUser, BookID: usUnlistedOnlyBook, Status: "in_progress", ProgressPct: 12},
	} {
		if err := s.SetUserBookState(st); err != nil {
			t.Fatalf("seed state: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return dbPath
}

func parseUserState(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "USERSTATE=") {
		t.Fatalf("want exactly one USERSTATE= line, got %q", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[0], "USERSTATE=")), &m); err != nil {
		t.Fatalf("the line is not JSON: %v: %q", err, lines[0])
	}
	return m
}

func noIDsIn(t *testing.T, text string) {
	t.Helper()
	for _, id := range []string{usListedProgressBook, usListedBlankBook, usUnlistedOnlyBook, usUnlistedUser, usSampleMissingBook, "_system", usSecretKeyInStrictEr} {
		if strings.Contains(text, id) {
			t.Errorf("the output names %q: %q", id, text)
		}
	}
}

var userStateClassRE = regexp.MustCompile(`^[a-z_]{1,32}$`)

// The happy path on a migrated store: the counts come from core's account list
// through the store's seam; state on a book with no book: row counts; a book
// whose state is only an unlisted user's counts as unlisted and not as
// progress; nothing names an id.
func TestUserState_CountsThroughTheStoresSeamOnAMigratedStore(t *testing.T) {
	userStateFixture(t)
	booksFile := filepath.Join(t.TempDir(), "books.txt")
	if err := os.WriteFile(booksFile, []byte(usListedProgressBook+"\n"+usSampleMissingBook+"\n\n"+usUnlistedOnlyBook+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runUserState(context.Background(), booksFile, &out)
	if err != nil {
		t.Fatalf("runUserState = %v; out %q", err, out.String())
	}
	m := parseUserState(t, out.String())
	t.Logf("%s", strings.TrimSpace(out.String()))
	num := func(k string) int {
		v, ok := m[k].(float64)
		if !ok {
			t.Fatalf("%s = %v, want a number", k, m[k])
		}
		return int(v)
	}
	if m["users_strict_err"] != "" {
		t.Errorf("users_strict_err = %v, want empty", m["users_strict_err"])
	}
	if num("users") < 1 || num("users") != num("users_strict") {
		t.Errorf("users %d, users_strict %d: want equal and at least core's system user", num("users"), num("users_strict"))
	}
	if num("system_users") != 1 {
		t.Errorf("system_users = %d, want 1 (core's _system)", num("system_users"))
	}
	if num("books_with_progress") != 2 || num("books_with_carryable_state") != 1 || num("books_with_unlisted_state") != 1 {
		t.Errorf("progress %d, carryable %d, unlisted %d; want 2, 1, 1", num("books_with_progress"), num("books_with_carryable_state"), num("books_with_unlisted_state"))
	}
	sample, ok := m["sample"].(map[string]any)
	if !ok {
		t.Fatalf("sample = %v", m["sample"])
	}
	if sample["asked"] != 3.0 || sample["has_progress"] != 1.0 || sample["has_carryable"] != 1.0 || sample["errors"] != 0.0 {
		t.Errorf("sample = %v, want asked 3, has_progress 1, has_carryable 1, errors 0", sample)
	}
	noIDsIn(t, out.String())
}

// The lead's minimal shape: one listed user's book and one book with state
// only from an unlisted user give progress 1, unlisted 1.
func TestUserState_UnlistedStateIsCountedApart(t *testing.T) {
	isolateCLIStore(t)
	restoreRootFlags(t)
	dbPath := os.Getenv("ABO_CORE_STORAGE_PATH")
	s, err := startedStore(t, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []*database.UserBookState{
		{UserID: "_system", BookID: usListedProgressBook, Status: "in_progress", ProgressPct: 41},
		{UserID: usUnlistedUser, BookID: usUnlistedOnlyBook, Status: "in_progress", ProgressPct: 12},
	} {
		if err := s.SetUserBookState(st); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runUserState(context.Background(), "", &out); err != nil {
		t.Fatalf("runUserState = %v; out %q", err, out.String())
	}
	m := parseUserState(t, out.String())
	if m["books_with_progress"] != 1.0 || m["books_with_unlisted_state"] != 1.0 {
		t.Errorf("progress %v, unlisted %v; want 1, 1", m["books_with_progress"], m["books_with_unlisted_state"])
	}
	if _, has := m["sample"]; has {
		t.Errorf("sample present without --books: %v", m["sample"])
	}
}

// A store the identity cutover has not run on is refused before any read: a
// non-zero exit, users_strict_err "unmigrated", every count null, and nothing
// under the store written.
func TestUserState_RefusesAnUnmigratedStoreAndWritesNothing(t *testing.T) {
	isolateCLIStore(t)
	restoreRootFlags(t)
	dbPath := os.Getenv("ABO_CORE_STORAGE_PATH")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := database.OpenStandalone(dbPath, database.StandaloneOptions{})
	if err != nil {
		t.Fatalf("a HEAD store: %v", err)
	}
	if err := s.SetUserBookState(&database.UserBookState{UserID: "u-head-bruce", BookID: usListedProgressBook, Status: "in_progress", ProgressPct: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, dbPath)

	var out bytes.Buffer
	err = runUserState(context.Background(), "", &out)
	var refusal errUserState
	if !errors.As(err, &refusal) || refusal.class != "unmigrated" {
		t.Fatalf("runUserState = %v, want the unmigrated refusal; out %q", err, out.String())
	}
	m := parseUserState(t, out.String())
	if m["users_strict_err"] != "unmigrated" {
		t.Errorf("users_strict_err = %v, want unmigrated", m["users_strict_err"])
	}
	for _, k := range []string{"users", "users_strict", "system_users", "books_with_progress", "books_with_carryable_state", "books_with_unlisted_state"} {
		if m[k] != nil {
			t.Errorf("%s = %v, want null on a refusal", k, m[k])
		}
	}
	// Pebble's read-only open takes the store's LOCK (its zero-byte lock file,
	// re-created on every open, so its mtime moves); that is the lock, not a
	// write to any storage level. Every other file must be byte-for-byte as
	// it was.
	if after := treeSnapshot(t, dbPath); !slices.Equal(withoutLockMtime(before), withoutLockMtime(after)) {
		t.Errorf("the refused probe changed the store:\nbefore %v\nafter  %v", before, after)
	}
}

// withoutLockMtime is a treeSnapshot with the LOCK file's mtime dropped (its
// name and size kept).
func withoutLockMtime(snap []string) []string {
	out := make([]string, 0, len(snap))
	for _, l := range snap {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "LOCK" {
			l = f[0] + " " + f[1]
		}
		out = append(out, l)
	}
	return out
}

// strictErrStore is the started store with ListUsersStrict failing as an
// undecodable row does, the error naming the row's key.
type strictErrStore struct{ *database.PebbleStore }

func (strictErrStore) ListUsersStrict() ([]database.User, error) {
	return nil, fmt.Errorf("%w: %s", database.ErrUndecodableUserRows, usSecretKeyInStrictEr)
}

// A strict-list error exits non-zero with a bare class and no count printed as
// 0, and the key the error names appears nowhere.
func TestUserState_StrictErrorIsABareClassAndNeverAZero(t *testing.T) {
	isolateCLIStore(t)
	restoreRootFlags(t)
	dbPath := os.Getenv("ABO_CORE_STORAGE_PATH")
	seedStarted(t, dbPath)
	orig := userStateOpen
	t.Cleanup(func() { userStateOpen = orig })
	userStateOpen = func(ctx context.Context) (userStateStore, func() error, error) {
		st, closeFn, err := orig(ctx)
		if err != nil {
			return nil, nil, err
		}
		return strictErrStore{st.(*database.PebbleStore)}, closeFn, nil
	}

	var out bytes.Buffer
	err := runUserState(context.Background(), "", &out)
	if err == nil {
		t.Fatalf("runUserState = nil, want a non-zero exit; out %q", out.String())
	}
	m := parseUserState(t, out.String())
	class, _ := m["users_strict_err"].(string)
	if class != "undecodable" || !userStateClassRE.MatchString(class) {
		t.Errorf("users_strict_err = %q, want the bare class undecodable", class)
	}
	if strings.Contains(out.String(), ":0") {
		t.Errorf("a count printed as 0 on an error: %q", out.String())
	}
	noIDsIn(t, out.String()+err.Error())
}

// --books must be 0600 or tighter; a wider file is refused before the store
// is opened.
func TestUserState_BooksFileModeIsChecked(t *testing.T) {
	booksFile := filepath.Join(t.TempDir(), "books.txt")
	if err := os.WriteFile(booksFile, []byte(usListedProgressBook+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(booksFile, 0o644); err != nil {
		t.Fatal(err)
	}
	orig := userStateOpen
	t.Cleanup(func() { userStateOpen = orig })
	userStateOpen = func(context.Context) (userStateStore, func() error, error) {
		t.Fatal("the store was opened before the --books file was checked")
		return nil, nil, nil
	}
	var out bytes.Buffer
	err := runUserState(context.Background(), booksFile, &out)
	var refusal errUserState
	if !errors.As(err, &refusal) || refusal.class != "books_file_mode" {
		t.Fatalf("runUserState = %v, want books_file_mode", err)
	}
	noIDsIn(t, out.String()+err.Error())
}
