// file: core/internal/bootseam/bootseam.go
// version: 1.0.0
// guid: 9d064296-a488-463e-93e9-38d3b21c9167
// last-edited: 2026-11-07

// Package bootseam is the per-boot seam of the crash-matrix kit
// (coretest/crashmatrix, 05-storage §5S.3.16; 06 §6.4.8): a context value
// that carries, for one in-process boot, the filesystem every boot writer
// uses (the store, its sidecars and checkpoints, the swap's journal and
// renames), the binary ID and clock the swap's rejection rule reads, and the
// labeller that names each durable operation for the writer table of 06
// §6.5.1. Production boots carry no Seams: every accessor then returns the
// real filesystem, no label and no override.
//
// It holds no package state (03 R5): the value travels in the context that
// app.New hands the kernel, so parallel boots in one test binary never share
// a seam.
package bootseam

import (
	"context"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// Seams is one boot's overrides; a nil field takes the production value.
type Seams struct {
	// FS is the filesystem of every boot writer; nil = vfs.Default.
	FS vfs.FS
	// BuildID replaces the binary ID (buildid.Binary) for the swap's
	// staged_by_build check and the store's build fields; "" = the real one.
	BuildID string
	// Clock replaces time.Now for the swap's expires_at check and the
	// store's timestamps; nil = time.Now.
	Clock func() time.Time
	// Labels names each durable operation; nil = no labels (production).
	Labels Labeler
}

// Labeler is the crash-matrix kit's view of a boot: Enter opens a region
// whose durable operations carry label until the returned exit is called.
// A region opened inside another is the outer one's: the outer label wins,
// so a caller that labels a call (restoreswap's "swap.sidecars" around
// storage.RewriteSidecars) labels every operation beneath it.
type Labeler interface {
	Enter(label string) (exit func())
}

type ctxKey struct{}

// With returns ctx carrying s.
func With(ctx context.Context, s *Seams) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// From is the Seams ctx carries; nil in production.
func From(ctx context.Context) *Seams {
	s, _ := ctx.Value(ctxKey{}).(*Seams)
	return s
}

// FS is the boot's filesystem: Seams.FS, else vfs.Default.
func FS(ctx context.Context) vfs.FS {
	if s := From(ctx); s != nil && s.FS != nil {
		return s.FS
	}
	return vfs.Default
}

// Region marks the durable operations until the returned exit as label's.
// A no-op without a Labeler.
func Region(ctx context.Context, label string) (exit func()) {
	return RegionOf(From(ctx), label)
}

// RegionOf is Region over Seams already read from a context (storage keeps
// them for the boot, so its writers need no context).
func RegionOf(s *Seams, label string) (exit func()) {
	if s == nil || s.Labels == nil {
		return func() {}
	}
	return s.Labels.Enter(label)
}

// Labelled runs fn inside label's region.
func Labelled(s *Seams, label string, fn func() error) error {
	exit := RegionOf(s, label)
	defer exit()
	return fn()
}
