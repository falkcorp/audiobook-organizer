// file: cmd/diagnostics_user_state.go
// version: 1.0.0
// guid: 859a1f79-9343-4f9b-a6dc-b319615084c9
// last-edited: 2026-11-28

package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	"github.com/falkcorp/mediacore/app"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// diagnostics user-state (F4-08 fix-forward; the F3-07 probe): with the
// service stopped, how many accounts the store's account list holds under core
// identity and how many books carry a user's listening state, read through the
// exact seam the merge and delete gates use (the store's ListUsersStrict over
// core's account list, merge.BookHasUserProgress and merge.UserStateProbe). It
// prints one line, USERSTATE={...}, and writes nothing anywhere.
//
// It opens the store read-only (Pebble read-only, through app.OpenOffline: the
// store guard, no migration, no cutover step, no boot writer, no seed) and runs
// no part of core's identity boot: core's accounts are read straight from the
// identity keyspace (app.Offline.Users). A store the identity cutover (M3) has
// not run on is refused as "unmigrated", from the signal a serve boot uses
// (the cutover's data migration, cutover.IdentityCutoverID, not done).
//
// No error ever prints as a count: a count that could not be read is null and
// the command exits non-zero. Error text never reaches the output, because a
// store error can name a key (ListUsersStrict's names the first bad row, the
// probe's name user and book ids): only a bare class is printed.

const userStateUsage = `Counts the accounts and the books with listening state, with the service stopped.

  diagnostics user-state [--books <file>]

The store is the one --db (or the config) names. It is opened read-only and
nothing is written. A store the identity cutover has not run on is refused
(users_strict_err "unmigrated"). --books names a file of book ids, one per
line, mode 0600 or tighter; the ids never appear in the output.

It prints one line:

  USERSTATE={"users": n, "users_strict": n, "system_users": n,
             "users_strict_err": "", "books_with_progress": n,
             "books_with_carryable_state": n,
             "books_with_unlisted_state": n, "sample": {...}}

and exits non-zero on any error, with a count it could not read as null.`

var userStateCmd = &cobra.Command{
	Use:           "user-state",
	Short:         "Count accounts and books with listening state, read-only, with the service stopped",
	Long:          userStateUsage,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		booksFile, err := cmd.Flags().GetString("books")
		if err != nil {
			return err
		}
		return runUserState(commandContext(cmd), booksFile, cmd.OutOrStdout())
	},
}

func init() {
	userStateCmd.Flags().String("books", "", "File of book ids to sample, one per line, mode 0600 or tighter")
	diagnosticsCmd.AddCommand(userStateCmd)
}

// userStateReport is the USERSTATE line. A pointer count is null when it was
// not read; it is never 0 for "could not tell".
type userStateReport struct {
	Users                   *int             `json:"users"`
	UsersStrict             *int             `json:"users_strict"`
	SystemUsers             *int             `json:"system_users"`
	UsersStrictErr          string           `json:"users_strict_err"`
	BooksWithProgress       *int             `json:"books_with_progress"`
	BooksWithCarryableState *int             `json:"books_with_carryable_state"`
	BooksWithUnlistedState  *int             `json:"books_with_unlisted_state"`
	Sample                  *userStateSample `json:"sample,omitempty"`
}

type userStateSample struct {
	Asked        int `json:"asked"`
	HasProgress  int `json:"has_progress"`
	HasCarryable int `json:"has_carryable"`
	Errors       int `json:"errors"`
}

// errUserState is the command's refusal: the class only, never the text.
type errUserState struct{ class string }

func (e errUserState) Error() string { return "diagnostics user-state: " + e.class }

// userStateOpen opens the probe's store; a var so a test can count its calls.
var userStateOpen = openUserStateStore

// userStateStore is what the probe reads: the books store with core's account
// list installed as books' Register installs it.
type userStateStore interface {
	merge.UserProgressMerger
	merge.UserStateReader
	ListUsersStrict() ([]database.User, error)
	ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error)
}

// openUserStateStore is openOfflineStore's read-only branch, plus core's
// account list: app.OpenOffline read-only, books' legacy range, the books
// store, and the user directory over the offline open's account read
// (legacyauth.UserDirectory, the mapping the serve path installs). Before any
// read it refuses a store the identity cutover has not run on.
func openUserStateStore(ctx context.Context) (userStateStore, func() error, error) {
	opts, err := offlineOptions()
	if err != nil {
		return nil, nil, err
	}
	// The owner list serve opens the store with under core identity: the
	// identity boot's owners (core/auth among them) are claimed only for an app
	// that declares it, and books' serve Options do (identityOptions). The
	// offline options do not, so without this the core/auth keyspace is not in
	// the open's owner list and core's accounts cannot be read. It changes the
	// owner list only: this open still runs no identity boot phase.
	opts.IdentityBoot = true
	o, err := app.OpenOffline(ctx, opts, true, books.OfflineModule(opts))
	if err != nil {
		return nil, nil, err
	}
	done, err := o.DataMigrationDone(ctx, cutover.IdentityCutoverID)
	if err != nil {
		return nil, nil, errors.Join(err, o.Close())
	}
	if !done {
		return nil, nil, errors.Join(errStoreUnmigrated, o.Close())
	}
	h, err := o.Storage(books.ID)
	if err != nil {
		return nil, nil, errors.Join(err, o.Close())
	}
	store, err := database.NewBooksStore(h.Legacy, nil)
	if err != nil {
		return nil, nil, errors.Join(err, o.Close())
	}
	store.SetUserDirectory(legacyauth.UserDirectory(o))
	return store, func() error { return errors.Join(store.Close(), o.Close()) }, nil
}

// errStoreUnmigrated: the identity cutover has not run on the store.
var errStoreUnmigrated = errors.New("the identity cutover has not run on this store")

// storeUnmigrated is whether err says the identity cutover (M3) has not run:
// the serve boot's signal (identityStoreReady: the cutover's data migration not
// done), read either after the open or, on a store with steps still to run,
// from the offline guard's refusal, which names the cutover among the pending
// steps.
func storeUnmigrated(err error) bool {
	if errors.Is(err, errStoreUnmigrated) {
		return true
	}
	var pend *storage.OfflinePendingError
	if errors.As(err, &pend) {
		for _, s := range pend.Steps {
			if s.DataMigration == cutover.IdentityCutoverID {
				return true
			}
		}
	}
	return false
}

// userStateClass is err as a bare class (^[a-z_]{1,32}$), never its text.
func userStateClass(err error) string {
	var pend *storage.OfflinePendingError
	switch {
	case err == nil:
		return ""
	case storeUnmigrated(err):
		return "unmigrated"
	case errors.As(err, &pend) && len(pend.Steps) > 0:
		return "pending_steps"
	case errors.As(err, &pend):
		return "store_not_started"
	case errors.Is(err, restorejournal.ErrSwapPending):
		return "swap_pending"
	case errors.Is(err, storage.ErrSwapLockHeld):
		return "swap_locked"
	case errors.Is(err, database.ErrUndecodableUserRows), errors.Is(err, coreauth.ErrUndecodableRecord):
		return "undecodable"
	case errors.Is(err, database.ErrNoUserDirectory), errors.Is(err, legacyauth.ErrNoUserDirectory):
		return "no_directory"
	case errors.Is(err, coreauth.ErrStoreUnavailable):
		return "store_unavailable"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	}
	return "store_error"
}

func intp(n int) *int { return &n }

func runUserState(ctx context.Context, booksFile string, stdout io.Writer) error {
	var rep userStateReport
	fail := func(class string) error {
		if err := writeUserState(stdout, rep); err != nil {
			return err
		}
		return errUserState{class}
	}

	var sampleIDs []string
	if booksFile != "" {
		ids, class := readSampleBooks(booksFile)
		if class != "" {
			return fail(class)
		}
		sampleIDs = ids
	}

	store, closeStore, err := userStateOpen(ctx)
	if err != nil {
		rep.UsersStrictErr = userStateClass(err)
		return fail(rep.UsersStrictErr)
	}
	defer func() { _ = closeStore() }()

	strict, err := store.ListUsersStrict()
	if err != nil {
		rep.UsersStrictErr = userStateClass(err)
		return fail(rep.UsersStrictErr)
	}
	rep.UsersStrict = intp(len(strict))
	system := 0
	listed := make(map[string]bool, len(strict))
	for _, u := range strict {
		listed[u.ID] = true
		if u.Status == string(coreauth.StatusSystem) {
			system++
		}
	}
	rep.SystemUsers = intp(system)
	users, err := store.ListUsers()
	if err != nil {
		return fail(userStateClass(err))
	}
	rep.Users = intp(len(users))

	probe, err := merge.NewUserStateProbe(store)
	if err != nil {
		return fail(userStateClass(err))
	}
	bookIDs, unlisted, err := stateBookIDs(store, listed)
	if err != nil {
		return fail(userStateClass(err))
	}
	progress, carryable, errs, firstErr := countUserState(ctx, store, probe, bookIDs)
	if errs > 0 {
		return fail(userStateClass(firstErr))
	}
	rep.BooksWithProgress, rep.BooksWithCarryableState = intp(progress), intp(carryable)
	rep.BooksWithUnlistedState = intp(unlisted)

	if sampleIDs != nil {
		p, c, e, _ := countUserState(ctx, store, probe, sampleIDs)
		rep.Sample = &userStateSample{Asked: len(sampleIDs), HasProgress: p, HasCarryable: c, Errors: e}
		if e > 0 {
			return fail("sample_errors")
		}
	}
	return writeUserState(stdout, rep)
}

func writeUserState(w io.Writer, rep userStateReport) error {
	raw, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "USERSTATE=%s\n", raw)
	return err
}

// readSampleBooks reads --books: distinct non-blank lines. The file must be
// mode 0600 or tighter. It returns a class, never the file's content.
func readSampleBooks(path string) ([]string, string) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, "books_file_unreadable"
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return nil, "books_file_mode"
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "books_file_unreadable"
	}
	defer f.Close()
	seen := map[string]bool{}
	ids := []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id := strings.TrimSpace(sc.Text())
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if sc.Err() != nil {
		return nil, "books_file_unreadable"
	}
	return ids, ""
}

// stateBookIDs is every distinct book id named by a user-state row KEY:
// ubs:<user>:<book> and upos:<user>:<book>:<segment>. Not book: rows: state on
// a book whose book: row is gone still counts, because it is still state a
// delete gate must see. A key of neither shape is an error, not a skip.
//
// unlisted is how many of those books have state rows ONLY from user ids the
// store's account list (listed) does not hold: state no gate reading the list
// can see, counted so a census before and after the flip compares orphans with
// orphans.
func stateBookIDs(store userStateStore, listed map[string]bool) (ids []string, unlisted int, err error) {
	// seen[book] is whether any of the book's rows so far is a listed user's.
	seen := map[string]bool{}
	for _, fam := range []struct {
		prefix string
		parts  int
	}{{"ubs:", 2}, {"upos:", 3}} {
		after := ""
		for {
			page, next, err := store.ScanPrefixPage(fam.prefix, after, 1000)
			if err != nil {
				return nil, 0, err
			}
			for _, kv := range page {
				rest := strings.TrimPrefix(kv.Key, fam.prefix)
				parts := strings.SplitN(rest, ":", fam.parts)
				if len(parts) != fam.parts || parts[0] == "" || parts[1] == "" {
					return nil, 0, fmt.Errorf("%w: a %s row of another shape", database.ErrUndecodableUserRows, fam.prefix)
				}
				hasListed, known := seen[parts[1]]
				if !known {
					ids = append(ids, parts[1])
				}
				seen[parts[1]] = hasListed || listed[parts[0]]
			}
			if next == "" || len(page) == 0 {
				break
			}
			after = next
		}
	}
	for _, hasListed := range seen {
		if !hasListed {
			unlisted++
		}
	}
	return ids, unlisted, nil
}

// countUserState asks both gates about each book over a bounded pool sized to
// the CPUs: each book's reads are independent Pebble reads (one per listed
// user), and a library has thousands of state books. It returns the counts,
// the number of books whose read failed and the first failure.
func countUserState(ctx context.Context, store userStateStore, probe *merge.UserStateProbe, ids []string) (progress, carryable, errs int, first error) {
	var p, c, e atomic.Int64
	var once sync.Once
	g, _ := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for _, id := range ids {
		g.Go(func() error {
			hasP, err := merge.BookHasUserProgress(store, id)
			if err == nil {
				var hasC bool
				hasC, err = probe.Has(id)
				if err == nil && hasC {
					c.Add(1)
				}
			}
			if err != nil {
				e.Add(1)
				once.Do(func() { first = err })
				return nil
			}
			if hasP {
				p.Add(1)
			}
			return nil
		})
	}
	_ = g.Wait()
	return int(p.Load()), int(c.Load()), int(e.Load()), first
}
