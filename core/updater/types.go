// file: core/updater/types.go
// version: 1.0.0
// guid: 7b2d9e41-3c68-4f0a-a5d7-1e6c0b84f3a2
// last-edited: 2026-11-18

package updater

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/storage"
)

// Options is supplied by the app's main through app.Options.Updater (05-updater
// §5U.3). A nil *Options means the app ships no updater: no service, no tasks,
// no routes, no component.
type Options struct {
	AppID       string // must equal the new binary's self-test app field
	BinaryName  string // the raw asset's prefix and the user agent's product
	Source      Source // the release source: the one parameter A-6 adds to the HEAD updater
	AssetName   func(r Release, goos, goarch string) string
	DefaultMode Mode // the app's default (ModeOff unless the app says otherwise)
}

// Mode is core.updater.mode.
type Mode uint8

// The modes. There is no mode that applies unattended (deferred-build, 5U.3).
const (
	ModeOff   Mode = iota // no network calls at all (the default for every app)
	ModeCheck             // scheduled check + notification; apply only by an admin, never unattended
)

// String is the config value: "off" or "check".
func (m Mode) String() string {
	if m == ModeCheck {
		return "check"
	}
	return "off"
}

// ParseMode reads a core.updater.mode value; ok is false for any other text.
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "off":
		return ModeOff, true
	case "check":
		return ModeCheck, true
	}
	return ModeOff, false
}

// Channel is core.updater.channel.
type Channel string

// The channels. "prerelease" replaces HEAD's "develop": a commit cannot be applied.
const (
	ChannelStable     Channel = "stable"
	ChannelPrerelease Channel = "prerelease"
)

// Source is a release feed.
type Source interface {
	// Latest is the newest release of ch, with its assets. A source with no
	// release yet answers ErrReleaseNotFound.
	Latest(ctx context.Context, ch Channel) (Release, error)
	// Open streams one asset of a release the source returned. size is -1
	// when the source does not know it.
	Open(ctx context.Context, a Asset) (rc io.ReadCloser, size int64, err error)
}

// TagSource is a Source that can also look one release up by its tag. A named
// apply and the fetch behind `make rollback-restore RELEASE=<tag>` need it:
// §5U.3's Source has only Latest, which cannot name an older release (the gap
// is reported in the F8-02 drift list). The github adapter implements it.
type TagSource interface {
	Source
	// Release is the release tagged version ("v" optional). A tag the source
	// does not have answers ErrReleaseNotFound.
	Release(ctx context.Context, version string) (Release, error)
}

// Env is what the updater hands a Source that wants it (Binder): the
// instance-scoped HTTP client, the user agent HEAD sent, and the way to read
// a named secret.
type Env struct {
	HTTP      *http.Client // nil: the source's own client
	UserAgent string       // "<BinaryName>/<current version>"
	// Secret reads the named core/secrets entry; nil means none can be read.
	Secret func(ctx context.Context, name string) (string, error)
}

// Binder is implemented by a Source that takes its HTTP client, user agent
// and secrets from the updater (so "HTTP through Deps.HTTP" holds without the
// Source interface naming providerhttp).
type Binder interface {
	Bind(Env) Source
}

// Release is one published release.
type Release struct {
	Version     string // semver, leading "v" optional
	Channel     Channel
	Notes       string
	URL         string
	PublishedAt time.Time
	Assets      []Asset
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// RawBinary returns HEAD's asset namer: the raw executable
// "<binary>-<goos>-<goarch>", which the updater writes straight to <exe>.new;
// no archive is opened (R10-8).
func RawBinary(binary string) func(Release, string, string) string {
	return func(_ Release, goos, goarch string) string {
		return fmt.Sprintf("%s-%s-%s", binary, goos, goarch)
	}
}

// Status is the updater's state, read lock-free.
type Status struct {
	Mode           Mode
	Channel        Channel
	Current        string
	Latest         string
	Available      bool // semver(Latest) > semver(Current); never a downgrade
	LastChecked    time.Time
	LastError      string // fixed token
	PreviousKept   string // version of <exe>.prev, if any (the manual rollback target)
	DisabledReason string // "off", "dev_build", "binary_not_writable"
	// LocalBuild: the current version carries a `git describe` suffix; Latest
	// is not compared with it.
	LocalBuild bool
	// AutoApplyPaused: the operator had asked for unattended apply, which this
	// updater does not do.
	AutoApplyPaused bool
}

// Deps are core services, injected by the kernel (05-updater §5U.3).
type Deps struct {
	HTTP        *providerhttp.Client // instance-scoped outbound client
	Restarter   Restarter            // kernel: Lifecycle.RequestRestart, graceful shutdown then restart
	Health      *health.Registry
	Events      *events.Bus
	Notify      *notify.Service  // stages core.update.found.v1 notifications (outbox-backed)
	Store       storage.Keyspace // c/updater/ (one key, Check)
	ReaderCheck ReaderCheck      // kernel-bound to 05-storage's guard decision (levels and the swap slot)
	Log         *slog.Logger
}

// ReaderCheck asks whether the binary at exe may serve the store at dbPath as
// it is now (05-updater §5U.3). nil = allowed; otherwise an error carrying
// one of its fixed reason tokens (see Refusal). NewReaderCheck is the
// in-process implementation.
type ReaderCheck func(ctx context.Context, dbPath, exe string) error

// Restarter requests the graceful restart that follows a swap.
type Restarter interface {
	Restart(ctx context.Context, reason string) error
}

// Settings are the core.updater.* values the updater reads (05-updater §5U.8),
// re-read on every operation so a hot-reloadable key takes effect at once. The
// kernel's core.updater section declares the same keys; the updater mirrors
// the ones it uses so it never imports the kernel.
type Settings struct {
	Mode        Mode
	Channel     Channel
	MaxDownload int64 // bytes; 0 = DefaultMaxDownload
	// AutoApplyPaused is the config cutover's mapping of ABO's
	// auto_update.enabled=true (5U.8): the operator asked for unattended apply.
	AutoApplyPaused bool
}

// DefaultMaxDownload is core.updater.max_download's default, 512 MiB.
const DefaultMaxDownload int64 = 512 << 20

// DefaultSettings are §5U.8's defaults for an app whose default mode is mode.
func DefaultSettings(mode Mode) Settings {
	return Settings{Mode: mode, Channel: ChannelStable, MaxDownload: DefaultMaxDownload}
}

func (s Settings) maxDownload() int64 {
	if s.MaxDownload <= 0 {
		return DefaultMaxDownload
	}
	return s.MaxDownload
}

// Target is what a manual apply installs: the release tag, and whether the
// operator named it (only a named target may be applied to a local build).
type Target struct {
	Version string // release tag, leading "v"
	Named   bool
}

// CheckOptions refine one check.
type CheckOptions struct {
	// Channel overrides core.updater.channel for this check; empty keeps it.
	Channel Channel
	// AutoApplyPaused marks the status as an unattended run's: the operator
	// asked for automatic apply and this build only checks.
	AutoApplyPaused bool
}

// refusal tokens, the fixed reasons ReaderCheck errors carry.
const (
	ReasonLevelNewer        = "level_newer"
	ReasonFormatNewer       = "format_newer"
	ReasonRestorePending    = "restore_pending"
	ReasonRestoreInflight   = "restore_inflight"
	ReasonSidecarUnreadable = "sidecar_unreadable"
	ReasonCannotDetermine   = "cannot_determine"
)

// Refusal is a ReaderCheck's refusal: one fixed Reason token and a message.
type Refusal struct {
	Reason string
	Detail string
	// Cause is the error the refusal wraps (restorejournal.ErrSwapPending for
	// a swap in the slot), so errors.Is still finds it.
	Cause error
}

func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString(r.Reason)
	if r.Detail != "" {
		b.WriteString(": ")
		b.WriteString(r.Detail)
	}
	return b.String()
}

func (r *Refusal) Unwrap() error { return r.Cause }
