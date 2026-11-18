// file: core/updater/apply.go
// version: 1.0.0
// guid: 0e5b7d94-2a16-4c83-9f0d-b8a43c61e7d5
// last-edited: 2026-11-18

package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/mediacore/tasks"
)

// swapKind is the Restarter reason of an apply; rollbackKind of a rollback.
const (
	restartReasonApply    = "updater.apply"
	restartReasonRollback = "updater.rollback"
)

// releaseFor returns the release tagged tag. A source that can look a tag up
// (TagSource) is asked directly; any other source can only offer its latest
// release, which must be that tag.
func (u *Updater) releaseFor(ctx context.Context, tag string) (Release, error) {
	if ts, ok := u.source.(TagSource); ok {
		rel, err := ts.Release(ctx, tag)
		if err != nil {
			return Release{}, err
		}
		return rel, nil
	}
	rel, err := u.source.Latest(ctx, u.settings().Channel)
	if err != nil {
		return Release{}, err
	}
	if got, ok := canonicalVersion(rel.Version); !ok || got != tag {
		return Release{}, fmt.Errorf("%w: %s", ErrReleaseNotFound, tag)
	}
	return rel, nil
}

// assetFor is the release's asset for this platform, or ErrNoAsset naming the
// file looked for.
func (u *Updater) assetFor(rel Release) (Asset, error) {
	if a := u.assetOf(rel); a != nil {
		return *a, nil
	}
	return Asset{}, fmt.Errorf("%w (looking for %s)", ErrNoAsset, u.opts.AssetName(rel, goos(), goarch()))
}

// download streams asset into path (mode 0755) capped at limit bytes and
// returns the bytes written. An oversize or short transfer, or any failure,
// deletes path (HEAD also deleted `.new` on a failed write).
func (u *Updater) download(ctx context.Context, a Asset, path string, limit int64) (int64, error) {
	if a.Size > limit {
		return 0, fmt.Errorf("%w: the asset is %d bytes, the cap is %d", ErrOversize, a.Size, limit)
	}
	rc, size, err := u.source.Open(ctx, a)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrDownloadFailed, err)
	}
	defer func() { _ = rc.Close() }()
	if size > limit {
		return 0, fmt.Errorf("%w: the asset is %d bytes, the cap is %d", ErrOversize, size, limit)
	}
	// One byte past the cap is read so a body that runs over is told apart from
	// one that ends exactly at it.
	n, err := writeExecutable(path, io.LimitReader(rc, limit+1))
	if err != nil {
		return n, fmt.Errorf("%w: %w", ErrDownloadFailed, err)
	}
	switch {
	case n > limit:
		u.removeQuietly(path)
		return n, fmt.Errorf("%w: the body ran past %d bytes", ErrOversize, limit)
	case size >= 0 && n != size:
		u.removeQuietly(path)
		return n, fmt.Errorf("%w: received %d of %d bytes", ErrDownloadFailed, n, size)
	}
	u.metrics.Downloaded(ctx, n)
	return n, nil
}

// removeQuietly deletes path; a file already gone is not worth a word.
func (u *Updater) removeQuietly(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		u.logf(slog.LevelWarn, "could not remove a partial file", "file", filepath.Base(path), "err", err)
	}
}

// Apply installs the release version names ("" = the release the last check
// offered) in place of the running binary and asks the Restarter for the
// restart onto it: HEAD's raw-asset download and rename swap with the
// hotfix's rules, the max_download cap and the --version-json self-test as
// the two additions (05-updater 5U.4). It is called only from the
// core.updater.apply task body, which no schedule starts.
//
// Every refusal happens before anything is downloaded; a failure after the
// download deletes <exe>.new; a swap that fails after its first rename puts
// <exe> back.
func (u *Updater) Apply(rc *tasks.RC, version string) error {
	ctx := rc.Context()
	if err := u.install(ctx, rc.Phase, version, true); err != nil {
		return err
	}
	end := rc.Phase("restart")
	defer end()
	// The task is uninterruptible from the first rename: a cancel from here on
	// must not stop the restart onto the binary that is now in place.
	if err := u.d.Restarter.Restart(context.WithoutCancel(ctx), restartReasonApply); err != nil {
		return fmt.Errorf("the update was applied but the restart failed (the process keeps running the old code until it is restarted): %w", err)
	}
	return nil
}

// Install is Apply without the restart and without a run: the same refusals,
// download, self-test and swap, then it returns and leaves the restart to the
// caller.
//
// Deprecated: it exists for the gin /api/v1/update/apply handler, which R0
// answers by installing and then exiting itself (internal/updater's adapter
// calls it with LegacySkipSelfTest). F8-04 deletes that handler and this
// method with it. The apply task is the only other way in.
func (u *Updater) Install(ctx context.Context, version string) error {
	return u.install(ctx, func(string) func() { return func() {} }, version, false)
}

// install is the pipeline of Apply and Install, up to and including the swap
// and the status update. needRestarter is true for Apply, which must be able
// to restart before it replaces anything.
func (u *Updater) install(ctx context.Context, phase func(string) func(), version string, needRestarter bool) error {
	set := u.settings()
	fail := func(outcome string, err error) error {
		u.metrics.Apply(ctx, outcome)
		return err
	}
	switch {
	case set.Mode < ModeCheck:
		return fail("refused", ErrDisabled)
	case needRestarter && u.d.Restarter == nil:
		return fail("refused", ErrNoRestarter)
	}

	end := phase("preconditions")
	tgt, err := u.Prepare(version)
	if err != nil {
		end()
		return fail("refused", err)
	}
	exe, err := u.executablePath()
	if err != nil {
		end()
		return fail("refused", fmt.Errorf("executable path: %w", err))
	}
	if err := u.probeWritable(); err != nil {
		end()
		u.markNotWritable()
		return fail("binary_not_writable", err)
	}
	if err := u.checkSwapSlot(); err != nil {
		end()
		return fail("refused", err)
	}
	end()

	end = phase("download")
	rel, err := u.releaseFor(ctx, tgt.Version)
	if err != nil {
		end()
		return fail("refused", fmt.Errorf("release %s: %w", tgt.Version, err))
	}
	asset, err := u.assetFor(rel)
	if err != nil {
		end()
		return fail("refused", err)
	}
	u.logf(slog.LevelInfo, "downloading update", "version", tgt.Version, "asset", asset.Name)
	newPath := exe + ".new"
	if _, err := u.download(ctx, asset, newPath, set.maxDownload()); err != nil {
		end()
		u.logf(slog.LevelWarn, "update download failed", "version", tgt.Version, "err", err)
		return fail("download_failed", err)
	}
	end()

	if !u.skipSelfTest {
		end = phase("selftest")
		if err := selfTest(ctx, newPath, u.opts.AppID, tgt.Version); err != nil {
			end()
			u.removeQuietly(newPath)
			u.logf(slog.LevelWarn, "update self-test failed", "version", tgt.Version, "err", err)
			return fail("selftest_failed", err)
		}
		end()
	}

	end = phase("swap")
	// Again just before the rename: a restore or reset staged while the release
	// downloaded must still find the binary that staged it.
	if err := u.checkSwapSlot(); err != nil {
		end()
		u.removeQuietly(newPath)
		return fail("refused", err)
	}
	if err := ctx.Err(); err != nil {
		end()
		u.removeQuietly(newPath)
		return fail("refused", err)
	}
	if err := u.swap(exe, newPath); err != nil {
		end()
		return fail("swap_failed", err)
	}
	end()
	u.metrics.Apply(ctx, "applied")
	u.logf(slog.LevelInfo, "update applied", "from", u.version, "to", tgt.Version)
	st := u.Status()
	st.PreviousKept, st.LastError = u.version, ""
	u.setStatus(st)
	return nil
}

// markNotWritable records binary_not_writable on the status.
func (u *Updater) markNotWritable() {
	st := u.Status()
	st.DisabledReason = "binary_not_writable"
	u.setStatus(st)
}

// swap moves exe to exe.prev and newPath to exe. On failure the running
// binary is left at exe.
func (u *Updater) swap(exe, newPath string) error {
	prevPath := exe + ".prev"
	if err := u.renameFile(exe, prevPath); err != nil {
		u.removeQuietly(newPath)
		return fmt.Errorf("failed to rename current binary: %w", err)
	}
	if err := u.renameFile(newPath, exe); err != nil {
		restoreErr := u.renameFile(prevPath, exe)
		u.removeQuietly(newPath)
		if restoreErr != nil {
			return fmt.Errorf("failed to rename new binary into place: %w (restoring the previous binary from %s also failed: %v)", err, prevPath, restoreErr)
		}
		return fmt.Errorf("failed to rename new binary into place: %w", err)
	}
	// Both renames succeeded: the update is applied. A failed directory fsync
	// is logged, not returned, so a completed swap is never reported as a
	// failure (which would skip the restart onto the new binary).
	if err := syncDir(filepath.Dir(exe)); err != nil {
		u.logf(slog.LevelWarn, "update applied but the binary directory could not be synced", "err", err)
	}
	return nil
}

// Rollback swaps the kept previous binary back and asks the Restarter for the
// restart: it runs ReaderCheck on <exe>.prev first and refuses with its
// reason; otherwise <exe> becomes <exe>.rolled-back (the name `make rollback`
// gives the binary it replaces) and <exe>.prev becomes <exe>. It is called only
// from the core.updater.rollback task body.
func (u *Updater) Rollback(rc *tasks.RC) error {
	ctx := rc.Context()
	refuse := func(err error) error {
		u.metrics.Rollback(ctx, "refused")
		return err
	}
	if u.d.Restarter == nil {
		return refuse(ErrNoRestarter)
	}
	end := rc.Phase("preconditions")
	exe, err := u.executablePath()
	if err != nil {
		end()
		return refuse(fmt.Errorf("executable path: %w", err))
	}
	prev := exe + ".prev"
	if fi, err := os.Stat(prev); err != nil || !fi.Mode().IsRegular() {
		end()
		return refuse(ErrNoPrevious)
	}
	if err := u.probeWritable(); err != nil {
		end()
		u.markNotWritable()
		return refuse(err)
	}
	if u.d.ReaderCheck == nil {
		end()
		return refuse(&Refusal{Reason: ReasonCannotDetermine, Detail: "the updater has no reader check, so it cannot tell whether the previous binary serves the store"})
	}
	if err := u.d.ReaderCheck(ctx, u.dbPath, prev); err != nil {
		end()
		return refuse(rollbackRefusal(err))
	}
	end()

	end = rc.Phase("swap")
	rolled := exe + ".rolled-back"
	if err := u.renameFile(exe, rolled); err != nil {
		end()
		return refuse(fmt.Errorf("failed to set the current binary aside: %w", err))
	}
	if err := u.renameFile(prev, exe); err != nil {
		restoreErr := u.renameFile(rolled, exe)
		end()
		if restoreErr != nil {
			return refuse(fmt.Errorf("failed to move the previous binary into place: %w (restoring the running binary from %s also failed: %v)", err, rolled, restoreErr))
		}
		return refuse(fmt.Errorf("failed to move the previous binary into place: %w", err))
	}
	if err := syncDir(filepath.Dir(exe)); err != nil {
		u.logf(slog.LevelWarn, "rollback done but the binary directory could not be synced", "err", err)
	}
	end()
	u.metrics.Rollback(ctx, "done")
	u.logf(slog.LevelInfo, "rolled back to the previous binary", "from", u.version)
	st := u.Status()
	st.PreviousKept = ""
	u.setStatus(st)

	end = rc.Phase("restart")
	defer end()
	if err := u.d.Restarter.Restart(context.WithoutCancel(ctx), restartReasonRollback); err != nil {
		return fmt.Errorf("the rollback was done but the restart failed (the process keeps running the old code until it is restarted): %w", err)
	}
	return nil
}

// rollbackRefusal adds to a ReaderCheck refusal what the operator does next: a
// previous binary older than a level the store reached cannot serve it, and
// only a restore goes back past that (05-backup).
func rollbackRefusal(err error) error {
	var r *Refusal
	if errors.As(err, &r) && (r.Reason == ReasonLevelNewer || r.Reason == ReasonFormatNewer) {
		return fmt.Errorf("%w; a binary swap cannot go back past a storage level step: use `make rollback-restore` (RELEASE=<tag> names the release to put back)", err)
	}
	return err
}

// FetchRelease downloads the one release named by version (never "latest", a
// URL or a path) into dest as apply's steps 2 and 3 do, and installs nothing:
// it touches neither <exe> nor <exe>.prev, opens no store and restarts
// nothing. It is the fetch behind `make rollback-restore RELEASE=<tag>`.
//
// The body goes to <dest>.part (mode 0755, fsynced, capped at the compiled-in
// DefaultMaxDownload: the command runs with the service stopped and reads no
// store, so no setting), is self-tested with --version-json (app == AppID,
// version == the tag) and only then renamed to dest. Any failure deletes the
// partial file and leaves dest absent.
func (u *Updater) FetchRelease(ctx context.Context, version, dest string) error {
	tag, ok := normalizeTag(version)
	if !ok || strings.EqualFold(strings.TrimSpace(version), "latest") {
		return ErrInvalidVersion
	}
	if strings.TrimSpace(dest) == "" {
		return errors.New("updater: fetch needs a destination path")
	}
	if exe, err := u.executablePath(); err == nil {
		if sameFile(dest, exe) || sameFile(dest, exe+".prev") {
			return errors.New("updater: the fetch destination is the running binary or its previous-binary slot; the fetch installs nothing")
		}
	}
	rel, err := u.releaseFor(ctx, tag)
	if err != nil {
		return fmt.Errorf("release %s: %w", tag, err)
	}
	asset, err := u.assetFor(rel)
	if err != nil {
		return err
	}
	part := dest + ".part"
	if _, err := u.download(ctx, asset, part, DefaultMaxDownload); err != nil {
		return err
	}
	if err := selfTest(ctx, part, u.opts.AppID, tag); err != nil {
		u.removeQuietly(part)
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		u.removeQuietly(part)
		return fmt.Errorf("could not move the fetched binary into place: %w", err)
	}
	if err := syncDir(filepath.Dir(dest)); err != nil {
		u.logf(slog.LevelWarn, "fetched binary is in place but its directory could not be synced", "err", err)
	}
	return nil
}

// sameFile reports whether a and b name the same path once made absolute.
func sameFile(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && aa == bb
}
