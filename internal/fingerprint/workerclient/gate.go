// file: internal/fingerprint/workerclient/gate.go
// version: 1.0.0
// guid: 35b498ed-d496-4677-81eb-adf09304d580
// last-edited: 2026-09-19

package workerclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// calibrationHeadBytes is how much of a calibration file the server hashes
// (acoustid.calibrationHeadBytes).
const calibrationHeadBytes = 64 << 10

// resolveRoots resolves every configured mount once (design (d2): the
// containment check compares against the resolved root) and requires each to
// be a directory.
func resolveRoots(roots map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(roots))
	for name, p := range roots {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, fmt.Errorf("%w: root %s=%s: %w", ErrConfig, name, p, err)
		}
		fi, err := os.Stat(r)
		if err != nil {
			return nil, fmt.Errorf("%w: root %s=%s: %w", ErrConfig, name, p, err)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("%w: root %s=%s is not a directory", ErrConfig, name, p)
		}
		out[name] = r
	}
	return out, nil
}

// checkMount is startup checks 1-3 of design (d2) for one root: the root
// lives on an allowed network filesystem (so an empty local directory cannot
// pose as the library), statfs reports it read-only, and a create in it
// fails. The last runs only after the second passed.
func (w *worker) checkMount(name, resolved string) error {
	mi, err := w.cfg.statMount(resolved)
	if err != nil {
		return fmt.Errorf("%w: root %s: %w", ErrMountCheck, name, err)
	}
	if !slices.ContainsFunc(w.cfg.AllowedFSTypes, func(t string) bool { return strings.EqualFold(t, mi.FSType) }) {
		return fmt.Errorf("%w: root %s (%s) is on a %q filesystem; allowed: %s (is the share mounted?)",
			ErrMountCheck, name, resolved, mi.FSType, strings.Join(w.cfg.AllowedFSTypes, ", "))
	}
	if mi.MountPoint != "" && !pathutil.IsWithin(resolved, mi.MountPoint) {
		return fmt.Errorf("%w: root %s (%s) is not under its reported mount point %s", ErrMountCheck, name, resolved, mi.MountPoint)
	}
	if !mi.ReadOnly {
		return fmt.Errorf("%w: root %s (%s, %s mount at %s) is not mounted read-only", ErrWritableMount, name, resolved, mi.FSType, mi.MountPoint)
	}
	if err := w.cfg.writeProbe(resolved); err != nil {
		if errors.Is(err, ErrWritableMount) {
			return err
		}
		return fmt.Errorf("%w: root %s: %w", ErrMountCheck, name, err)
	}
	return nil
}

// checkHello verifies what the server announced against this worker: the
// pipeline, the tool pair, and that every configured root is one the server
// offers to remote workers.
func (w *worker) checkHello(h *workerapi.HelloResponse) error {
	if h.Pipeline != fingerprint.WindowPipelineID {
		return fmt.Errorf("%w: server pipeline %q, this worker runs %q", ErrToolsNotAllowed, h.Pipeline, fingerprint.WindowPipelineID)
	}
	ok := slices.ContainsFunc(h.ToolVersions, func(v workerapi.ToolVersions) bool {
		return v.Fpcalc == w.cfg.Versions.Fpcalc && v.FFmpeg == w.cfg.Versions.FFmpeg
	})
	if !ok {
		return fmt.Errorf("%w: this worker has fpcalc %s + ffmpeg %s; the server allows %s",
			ErrToolsNotAllowed, w.cfg.Versions.Fpcalc, w.cfg.Versions.FFmpeg, describePairs(h.ToolVersions))
	}
	for name := range w.roots {
		i := slices.IndexFunc(h.Roots, func(r workerapi.Root) bool { return r.ID == name })
		switch {
		case i < 0:
			return fmt.Errorf("%w: root %q is not one the server knows (server roots: %s)", ErrConfig, name, describeRoots(h.Roots))
		case !h.Roots[i].Remote:
			return fmt.Errorf("%w: the server does not offer root %q to remote workers", ErrConfig, name)
		}
	}
	return nil
}

func describePairs(v []workerapi.ToolVersions) string {
	if len(v) == 0 {
		return "none"
	}
	parts := make([]string, len(v))
	for i, p := range v {
		parts[i] = "fpcalc " + p.Fpcalc + " + ffmpeg " + p.FFmpeg
	}
	return strings.Join(parts, "; ")
}

func describeRoots(r []workerapi.Root) string {
	names := make([]string, len(r))
	for i, x := range r {
		names[i] = x.ID
	}
	return strings.Join(names, ", ")
}

// calibrationTargets returns the calibration files under configured roots
// and requires at least one per configured root: without one, neither the
// root mapping nor the pipeline parity can be proven.
func (w *worker) calibrationTargets(h *workerapi.HelloResponse) ([]workerapi.CalibrationFile, error) {
	var out []workerapi.CalibrationFile
	seen := map[string]bool{}
	for _, cf := range h.Calibration {
		if _, ok := w.roots[cf.Root]; !ok || len(cf.Windows) == 0 {
			continue
		}
		out = append(out, cf)
		seen[cf.Root] = true
	}
	for name := range w.roots {
		if !seen[name] {
			return nil, fmt.Errorf("%w: the server offered no calibration file under root %q, so the root mapping and pipeline parity cannot be proven (the window backfill must have server-cut windows first)", ErrParity, name)
		}
	}
	return out, nil
}

// openCalibration resolves one calibration file and checks it is the same
// file the server has: same size and mtime, same SHA-256 over the first
// 64 KiB (hashed exactly as the server does, tolerating a shorter file).
func (w *worker) openCalibration(cf workerapi.CalibrationFile) (string, error) {
	relB, err := base64.StdEncoding.DecodeString(cf.RelB64)
	if err != nil {
		return "", fmt.Errorf("calibration %s: rel_b64 is not base64", cf.Rel)
	}
	p, err := w.resolve(w.roots[cf.Root], string(relB))
	if err != nil {
		return "", fmt.Errorf("calibration %s/%s: %s", cf.Root, cf.Rel, w.scrub(err.Error()))
	}
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("calibration %s/%s: %s", cf.Root, cf.Rel, w.scrub(err.Error()))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("calibration %s/%s: %s", cf.Root, cf.Rel, w.scrub(err.Error()))
	}
	if fi.Size() != cf.Size || fi.ModTime().Unix() != cf.MtimeUnix {
		return "", fmt.Errorf("calibration %s/%s: size/mtime %d/%d here, %d/%d on the server: the root mapping points at a different tree",
			cf.Root, cf.Rel, fi.Size(), fi.ModTime().Unix(), cf.Size, cf.MtimeUnix)
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, calibrationHeadBytes); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("calibration %s/%s: read: %s", cf.Root, cf.Rel, w.scrub(err.Error()))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != cf.Head64K {
		return "", fmt.Errorf("calibration %s/%s: first 64 KiB differ from the server's copy: the root mapping points at a different file", cf.Root, cf.Rel)
	}
	return p, nil
}

// checkCalibrationFiles is the identity half of the gate, also re-run by the
// periodic re-check: every calibration file resolves to the server's bytes.
func (w *worker) checkCalibrationFiles(cfs []workerapi.CalibrationFile) ([]string, error) {
	paths := make([]string, len(cfs))
	for i, cf := range cfs {
		p, err := w.openCalibration(cf)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMountCheck, err)
		}
		paths[i] = p
	}
	return paths, nil
}

// parity is the byte-parity gate of design (d): every server-cut calibration
// window, cut again here, must hash to the server's stored raw print and have
// the same frame count. Any difference, however small, stops the worker:
// prints that are merely similar would be stored as if the server had made
// them.
func (w *worker) parity(ctx context.Context, cfs []workerapi.CalibrationFile, paths []string, windowSet string) error {
	var mu sync.Mutex
	var problems []string
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(w.cfg.Concurrency)
	for i, cf := range cfs {
		for _, cw := range cf.Windows {
			g.Go(func() error {
				spec := fingerprint.WindowSpec{Kind: fingerprint.WindowKind(cw.Kind), SlotBP: cw.SlotBP,
					OffsetSec: cw.OffsetSec, LengthSec: cw.LengthSec, CoversWhole: cw.CoversWhole, WindowSet: windowSet}
				wp, err := w.cutWithRetry(gctx, paths[i], spec)
				var msg string
				switch {
				case err != nil:
					msg = fmt.Sprintf("%s/%s slot %d: cut failed: %s", cf.Root, cf.Rel, cw.SlotBP, w.scrub(err.Error()))
				default:
					sum := sha256.Sum256(wp.Raw)
					got := hex.EncodeToString(sum[:])
					if got != cw.RawSHA256 || wp.Frames != cw.Frames {
						msg = fmt.Sprintf("%s/%s slot %d @%.3fs: sha256 %s / %d frames here, %s / %d frames on the server (cut by fpcalc %s + ffmpeg %s)",
							cf.Root, cf.Rel, cw.SlotBP, cw.OffsetSec, got, wp.Frames, cw.RawSHA256, cw.Frames, cw.Fpcalc, cw.FFmpeg)
					}
				}
				if msg != "" {
					mu.Lock()
					problems = append(problems, msg)
					mu.Unlock()
				}
				return nil
			})
		}
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return fmt.Errorf("%w: this worker (fpcalc %s + ffmpeg %s) does not reproduce the server's prints byte for byte; refusing to run:\n  %s",
			ErrParity, w.cfg.Versions.Fpcalc, w.cfg.Versions.FFmpeg, strings.Join(problems, "\n  "))
	}
	return nil
}

// recheck re-runs the mount checks and the calibration identity check (not
// the parity cut): design (d2) "Re-checks".
func (w *worker) recheck() error {
	for name, r := range w.roots {
		if err := w.checkMount(name, r); err != nil {
			return err
		}
	}
	_, err := w.checkCalibrationFiles(w.calib)
	return err
}
