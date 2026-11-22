// file: internal/plugins/deluge/path_update.go
// version: 1.5.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-11-19

package deluge

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// pathUpdateKind is rule R3's native kind for deluge.path-update.
const pathUpdateKind = tasks.Kind("books.deluge.path-update")

// pathUpdateHelp is HEAD's Description and the native Help (rule R5).
const pathUpdateHelp = "Updates a torrent's storage path in Deluge after a book is relocated: moves each active version's " +
	"torrent to the book's folder with core.move_storage (only when deluge_move_enabled is on). " +
	"dry_run defaults to true: a run that states no mode logs and counts the moves it would make and calls Deluge " +
	"for none of them. Pass dry_run=false to move."

func (p *Plugin) pathUpdateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "deluge.path-update",
		Preview:         sdk.PreviewSpec{Params: pathUpdateParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "deluge",
		DisplayName:     "Update Deluge torrent path",
		Description:     pathUpdateHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "deluge.path-update",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Minute,
		Run:             p.runPathUpdate,
		// CapFilesWrite: core.move_storage relocates the torrent's files on
		// disk. CapNetworkGeneric: the call goes to the Deluge Web UI.
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapFilesWrite,
			sdk.CapNetworkGeneric,
		},
	}
}

// pathUpdateParams is the payload for path-update operations.
type pathUpdateParams struct {
	BookID string `json:"book_id"`
	// DryRun (dry_run) and DryRunCamel (dryRun) are one switch, resolved by
	// opmode.ResolveDryRun: omitted means preview, false moves torrents, and
	// the two spellings disagreeing is an error. A preview never calls
	// MoveStorage.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// pathUpdateStore is the reads the core takes.
type pathUpdateStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
}

// storageMover is the one Deluge call the core makes: core.move_storage. It is
// not a store write; it stays in the core (F5-05 brief).
type storageMover interface {
	MoveStorage(torrentIDs []string, destPath string) error
}

// pathUpdateCoreParams is the core's params: HEAD's pathUpdateParams without
// the mode switch (the mode is the env's Live).
type pathUpdateCoreParams struct {
	BookID string `json:"book_id"`
}

// pathUpdateEnv is what the core takes from its shell. Mover is nil when Deluge
// is not configured (HEAD's p.client == nil). There is no Writer: the only
// effect is the Deluge move, which is the client's.
type pathUpdateEnv struct {
	kindEnv
	Cfg   *config.Config
	Store pathUpdateStore
	Mover storageMover
	// Sleep waits d between two attempts of one torrent's move and returns the
	// context's error as soon as ctx is done. Nil is ctxSleep. A test sets it
	// to record the delays without waiting.
	Sleep func(ctx context.Context, d time.Duration) error
}

// moveAttempts bounds one torrent's MoveStorage calls in a run, the first
// included; moveBackoff is the wait before the second and the third. The retry
// is inside the run because an outbox delivery is acked when the run is staged
// (outbox ConsumeIntoTask), so nothing redelivers a run that ends failed.
const (
	moveAttempts = 3
	moveBackoff  = time.Second
)

// ctxSleep waits d, or until ctx is done (then it returns ctx's error).
func ctxSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// moveWithRetry calls MoveStorage up to moveAttempts times for one torrent. It
// retries only an error delugeclient.IsTransient accepts, waiting moveBackoff
// and then twice that between attempts; a permanent refusal (an unknown
// torrent, a refused path) is returned at once. A done ctx stops it with the
// context's error, not the last move error.
func moveWithRetry(ctx context.Context, env pathUpdateEnv, hash, dir string) error {
	sleep := env.Sleep
	if sleep == nil {
		sleep = ctxSleep
	}
	var err error
	for attempt := 1; attempt <= moveAttempts; attempt++ {
		if err = env.Mover.MoveStorage([]string{hash}, dir); err == nil {
			return nil
		}
		if !delugeclient.IsTransient(err) || attempt == moveAttempts {
			return err
		}
		env.Logger.Warn("move_storage failed, retrying",
			"hash", hash, "path", dir, "attempt", attempt, "error", err)
		if serr := sleep(ctx, moveBackoff<<(attempt-1)); serr != nil {
			return serr
		}
	}
	return err
}

// pathUpdateResult is the native run's result. HEAD stored none.
type pathUpdateResult struct {
	BookID  string `json:"book_id"`
	DryRun  bool   `json:"dry_run"`
	Planned int64  `json:"planned"`
	Updated int64  `json:"updated"`
}

// pathUpdateParallelism is the pool of the version loop: HEAD's runitems:sequential
// call (one book's versions, a handful).
func pathUpdateParallelism() tasks.Parallelism {
	return tasks.Sequential("one book's versions (a handful); sequential by design")
}

// pathUpdateCore is the body of runPathUpdate after its params and mode are
// decoded, shared by both shells.
func pathUpdateCore(ctx context.Context, args pathUpdateCoreParams, env pathUpdateEnv) (pathUpdateResult, error) {
	cfg := env.Cfg
	dryRun := !env.Live

	bookID := args.BookID
	if bookID == "" {
		return pathUpdateResult{}, fmt.Errorf("book_id is required")
	}

	newProgress(env.kindEnv, 0).Start(fmt.Sprintf("Loading book %s...", bookID))

	book, err := env.Store.GetBookByID(bookID)
	if err != nil {
		return pathUpdateResult{}, fmt.Errorf("load book: %w", err)
	}
	if book == nil {
		return pathUpdateResult{}, fmt.Errorf("book not found")
	}

	versions, err := env.Store.GetBookVersionsByBookID(bookID)
	if err != nil {
		return pathUpdateResult{}, fmt.Errorf("load versions: %w", err)
	}

	progress := newProgress(env.kindEnv, len(versions))
	if dryRun {
		progress.Start(fmt.Sprintf("DRY RUN: checking %d version(s) against Deluge, nothing will be moved...", len(versions)))
	} else {
		progress.Start(fmt.Sprintf("Updating %d version(s) in Deluge...", len(versions)))
	}

	// Pre-compute active count so the item closure can reference it without
	// iterating versions again on each call.
	activeCount := 0
	for _, v := range versions {
		if v.Status == database.BookVersionStatusActive {
			activeCount++
		}
	}
	primaryDir := filepath.Dir(book.FilePath)

	var updated, planned atomic.Int64
	var failMu sync.Mutex
	var failed []string // hashes of the torrents whose move failed for good

	// runitems:sequential one book's versions (a handful); sequential by design
	if err := env.Items(ctx, pathUpdateParallelism(), len(versions), kindItemsOptions{Collect: true}, func(ctx context.Context, i int) error {
		v := versions[i]
		if v.TorrentHash == "" || v.Status != database.BookVersionStatusActive {
			return nil // skip non-Deluge or inactive versions
		}

		var dir string
		if activeCount == 1 {
			dir = primaryDir
		} else {
			dir = filepath.Join(primaryDir, ".versions", v.ID)
		}

		if cfg.DelugeMoveEnabled && env.Mover != nil {
			if dryRun {
				env.Logger.Info("would move_storage",
					"hash", v.TorrentHash, "path", dir)
				planned.Add(1)
				return nil
			}
			if err := moveWithRetry(ctx, env, v.TorrentHash, dir); err != nil {
				if ctx.Err() != nil {
					return ctx.Err() // canceled or timed out: not this torrent's failure
				}
				env.Logger.Error("move_storage failed",
					"hash", v.TorrentHash, "path", dir, "error", err)
				failMu.Lock()
				failed = append(failed, v.TorrentHash)
				failMu.Unlock()
				return nil // the other versions are still tried; the run reports the failures below
			}
			env.Logger.Info("move_storage succeeded",
				"hash", v.TorrentHash, "path", dir)
			updated.Add(1)
		}
		return nil
	}); err != nil {
		return pathUpdateResult{}, err
	}

	env.Logger.Info("deluge path-update complete",
		"book_id", bookID, "dry_run", dryRun, "planned", planned.Load(), "updated", updated.Load())
	mode := "APPLY"
	if dryRun {
		mode = "DRY RUN"
	}
	progress.Done(fmt.Sprintf("%s: %d move_storage planned, %d updated", mode, planned.Load(), updated.Load()))
	res := pathUpdateResult{BookID: bookID, DryRun: dryRun, Planned: planned.Load(), Updated: updated.Load()}
	if len(failed) > 0 {
		// The run ends failed and visible instead of reporting success for a
		// torrent that never followed its book; a fresh run starts from zero
		// (ResumeFromZero) and moves it. Hashes only, never paths.
		return res, fmt.Errorf("move_storage failed for %d torrent(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return res, nil
}

// runPathUpdate is the v2 shell: HEAD's Run (params, the dry_run/dryRun
// switch) over pathUpdateCore.
func (p *Plugin) runPathUpdate(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	var args pathUpdateParams
	if err := json.Unmarshal(params, &args); err != nil {
		return fmt.Errorf("unmarshal params: %w", err)
	}

	dryRun, err := opmode.ResolveDryRun("deluge.path-update", args.DryRun, args.DryRunCamel)
	if err != nil {
		return err
	}

	env := pathUpdateEnv{
		kindEnv: headEnv(reporter, !dryRun),
		Cfg:     &config.AppConfig,
		Store:   p.headStore(),
	}
	if c := p.headClient(); c != nil {
		env.Mover = c
	}
	_, err = pathUpdateCore(ctx, pathUpdateCoreParams{BookID: args.BookID}, env)
	return err
}
