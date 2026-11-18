// file: internal/updater/compat.go
// version: 1.0.1
// guid: 6f1c2d3e-4a5b-4c6d-8e7f-9a0b1c2d3e4f
// last-edited: 2026-11-18

package updater

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	coreupdater "github.com/falkcorp/mediacore/updater"
)

// updateLog routes the scheduler's lines through internal/logger's sanitizing
// layer: versions, tags and asset URLs come from the release source.
var updateLog = logger.New("updater")

// The R0 updater moved to core/updater (F8-02). The names the server and the
// scheduler use are the moved code's own until the adapter replaces them.
type (
	Updater    = coreupdater.Updater
	UpdateInfo = coreupdater.UpdateInfo
	Option     = coreupdater.Option
)

const (
	RestartExitCode = coreupdater.RestartExitCode
	LocalBuildNote  = coreupdater.LocalBuildNote
)

var (
	ErrNoUpdate               = coreupdater.ErrNoUpdate
	ErrLocalBuildNeedsVersion = coreupdater.ErrLocalBuildNeedsVersion
	ErrNotNewer               = coreupdater.ErrNotNewer
	ErrUnversioned            = coreupdater.ErrUnversioned
	ErrInvalidVersion         = coreupdater.ErrInvalidVersion
	ErrReleaseNotFound        = coreupdater.ErrReleaseNotFound
	ErrSwapBusy               = restorejournal.ErrSwapPending
	ErrNoStateDir             = coreupdater.ErrNoStateDir

	WithStateDir          = coreupdater.WithStateDir
	WithAPIBaseForTest    = coreupdater.WithAPIBaseForTest
	WithHTTPClientForTest = coreupdater.WithHTTPClientForTest
	WithExecutableForTest = coreupdater.WithExecutableForTest
	WithExitForTest       = coreupdater.WithExitForTest
	IsLocalBuild          = coreupdater.IsLocalBuild
)

// NewUpdater is core's, with this app's sanitising logger.
func NewUpdater(currentVersion string, opts ...Option) *Updater {
	base := []Option{
		coreupdater.WithLogger(logger.FromContext(context.Background()).With("subsystem", "updater")),
	}
	return coreupdater.NewUpdater(currentVersion, append(base, opts...)...)
}
