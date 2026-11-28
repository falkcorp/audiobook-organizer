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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
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

// captureProcessOutput runs fn with the process's stdout and stderr redirected,
// and returns what each received: what an operator's shell sees.
func captureProcessOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	read := func(f **os.File) (func() string, func()) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := *f
		*f = w
		done := make(chan string)
		go func() {
			b, _ := io.ReadAll(r)
			done <- string(b)
		}()
		return func() string { _ = w.Close(); *f = orig; return <-done }, func() { *f = orig }
	}
	outDone, outRestore := read(&os.Stdout)
	errDone, errRestore := read(&os.Stderr)
	defer outRestore()
	defer errRestore()
	fn()
	return outDone(), errDone()
}

// The probe as an operator runs it, through the root command with --db after
// the subcommand (the lead's command line): stdout is exactly the one
// USERSTATE line, nothing on either stream names an id, and a refusal is an
// error, which main turns into a non-zero exit.
func TestUserState_RunsAsTheCommand(t *testing.T) {
	dbPath := userStateFixture(t)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	origCfgFile, origDB, origConfig := cfgFile, databasePath, config.Snapshot()
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		config.SetForTest(func(c *config.Config) { cfgFile, databasePath, *c = origCfgFile, origDB, origConfig })
	})

	var err error
	stdout, stderr := captureProcessOutput(t, func() {
		rootCmd.SetArgs([]string{"diagnostics", "user-state", "--config", cfg, "--db", dbPath})
		err = rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("diagnostics user-state = %v\nstdout %q\nstderr %q", err, stdout, stderr)
	}
	m := parseUserState(t, stdout)
	if m["books_with_progress"] != 2.0 || m["users_strict_err"] != "" {
		t.Errorf("the command's line = %q", stdout)
	}
	noIDsIn(t, stdout+stderr)

	// The same command on a store the cutover has not run on refuses.
	unmigrated := filepath.Join(t.TempDir(), "head", "books.pebble")
	if err := os.MkdirAll(filepath.Dir(unmigrated), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABO_CORE_STORAGE_PATH", unmigrated)
	s, err := database.OpenStandalone(unmigrated, database.StandaloneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserBookState(&database.UserBookState{UserID: "u-head-bruce", BookID: usListedProgressBook, Status: "in_progress", ProgressPct: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, stderr = captureProcessOutput(t, func() {
		rootCmd.SetArgs([]string{"diagnostics", "user-state", "--config", cfg, "--db", unmigrated})
		err = rootCmd.Execute()
	})
	var refusal errUserState
	if !errors.As(err, &refusal) || refusal.class != "unmigrated" {
		t.Fatalf("on an unmigrated store = %v, want the unmigrated refusal\nstdout %q\nstderr %q", err, stdout, stderr)
	}
	if parseUserState(t, stdout)["users_strict_err"] != "unmigrated" {
		t.Errorf("the refusal's line = %q", stdout)
	}
	noIDsIn(t, stdout+stderr+err.Error())
}

// openOfflineStore installs no user directory: on a migrated store (core holds
// _system, M3 drained u:) its account list is an error, never an empty list,
// so an offline caller of a merge or delete gate stops instead of reading
// "nobody has state". The store marks itself because its spec retires u:
// (store/user_directory.go).
func TestOpenOfflineStore_AMigratedStoresUserListIsAnErrorNeverEmpty(t *testing.T) {
	userStateFixture(t)
	st, closeStore, err := openOfflineStore(context.Background(), true)
	if err != nil {
		t.Fatalf("openOfflineStore: %v", err)
	}
	t.Cleanup(closeStore)
	lister, ok := st.(interface {
		ListUsers() ([]database.User, error)
		ListUsersStrict() ([]database.User, error)
	})
	if !ok {
		t.Fatalf("the offline store %T lists no users", st)
	}
	for name, list := range map[string]func() ([]database.User, error){"ListUsers": lister.ListUsers, "ListUsersStrict": lister.ListUsersStrict} {
		users, err := list()
		if !errors.Is(err, database.ErrNoUserDirectory) || users != nil {
			t.Errorf("%s = %d users, %v; want ErrNoUserDirectory and no list", name, len(users), err)
		}
	}
}

// migrationState answers DataMigrationDone as configured and records the id.
type migrationState struct {
	done  bool
	err   error
	asked string
}

func (m *migrationState) DataMigrationDone(_ context.Context, id string) (bool, error) {
	m.asked = id
	return m.done, m.err
}

// The probe's own refusal of a store the identity cutover has not run on, the
// check behind the offline guard: not done is errStoreUnmigrated (class
// unmigrated), an unreadable state is that read's error, and done passes. The
// guard refuses every such store first through app.OpenOffline (its pending
// steps name the cutover), so this check is pinned here, below the open.
func TestUserState_RequireIdentityCutoverRefusesNotDone(t *testing.T) {
	m := &migrationState{}
	if err := requireIdentityCutover(context.Background(), m); !errors.Is(err, errStoreUnmigrated) || userStateClass(err) != "unmigrated" {
		t.Errorf("not done: %v (class %q), want errStoreUnmigrated (unmigrated)", err, userStateClass(err))
	}
	if m.asked != cutover.IdentityCutoverID {
		t.Errorf("asked about %q, want the identity cutover", m.asked)
	}
	boom := errors.New("migration state unreadable")
	if err := requireIdentityCutover(context.Background(), &migrationState{err: boom}); !errors.Is(err, boom) {
		t.Errorf("unreadable: %v, want the read's error", err)
	}
	if err := requireIdentityCutover(context.Background(), &migrationState{done: true}); err != nil {
		t.Errorf("done: %v", err)
	}
}

// An error no class names falls to store_error, and its text, a key here,
// never reaches the output: as a class, and through the probe's strict read.
func TestUserState_AnUnnamedErrorIsStoreErrorWithoutItsText(t *testing.T) {
	secret := fmt.Errorf("pebble said: %s is wedged", usSecretKeyInStrictEr)
	if got := userStateClass(secret); got != "store_error" {
		t.Errorf("userStateClass = %q, want store_error", got)
	}

	isolateCLIStore(t)
	restoreRootFlags(t)
	seedStarted(t, os.Getenv("ABO_CORE_STORAGE_PATH"))
	orig := userStateOpen
	t.Cleanup(func() { userStateOpen = orig })
	userStateOpen = func(ctx context.Context) (userStateStore, func() error, error) {
		st, closeFn, err := orig(ctx)
		if err != nil {
			return nil, nil, err
		}
		return plainErrStore{st.(*database.PebbleStore), secret}, closeFn, nil
	}
	var out bytes.Buffer
	err := runUserState(context.Background(), "", &out)
	if err == nil {
		t.Fatalf("runUserState = nil, want a non-zero exit; out %q", out.String())
	}
	m := parseUserState(t, out.String())
	if class, _ := m["users_strict_err"].(string); class != "store_error" {
		t.Errorf("users_strict_err = %q, want store_error", class)
	}
	if strings.Contains(out.String()+err.Error(), "wedged") {
		t.Errorf("the error's text reached the output: %q / %v", out.String(), err)
	}
	noIDsIn(t, out.String()+err.Error())
}

// plainErrStore fails ListUsersStrict with an error no class names.
type plainErrStore struct {
	*database.PebbleStore
	err error
}

func (s plainErrStore) ListUsersStrict() ([]database.User, error) { return nil, s.err }
