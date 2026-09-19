// file: internal/fingerprint/workerclient/config.go
// version: 1.1.0
// guid: c3d37f87-961c-4851-b773-40558ec854f9
// last-edited: 2026-09-19

// Package workerclient is the fp-worker client: a process on another host
// (an Apple Silicon Mac in practice) that leases windowed-fingerprint jobs
// from the server's remote worker API (/api/v1/fingerprint/worker/*, package
// internal/server/handlers/fpworker), cuts the windows from its own read-only
// mount of the library with the same ffmpeg | fpcalc pipeline the server uses
// (fingerprint.WindowTools.FileWindow), and posts the prints back.
//
// Design: .claude/notes/windowed-fingerprint-design-2026-09-12.md sections
// (d) and (d2). Before it leases anything the worker proves three things and
// exits non-zero if any fails: every mapped root is a read-only network mount
// (statfs + a write probe); its tool pair and pipeline are ones the server
// allowlists; and it reproduces the server's stored prints of the calibration
// files byte for byte (the parity gate).
package workerclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// KeyEnvVar is the environment variable the API key may be read from when no
// key file is given. The key is never accepted as a flag value.
const KeyEnvVar = "AO_FP_WORKER_KEY"

// RootsEnvVar holds the root map ("libroot=/Volumes/x,other=/y") when no
// --root flag is given.
const RootsEnvVar = "AO_FP_WORKER_ROOTS"

// DefaultConcurrency is how many jobs run at once by default.
const DefaultConcurrency = 8

// Errors Run returns; each makes the process exit non-zero.
var (
	// ErrWritableMount: a mapped root is not a read-only mount.
	ErrWritableMount = errors.New("fp-worker: mapped root is writable; mount it read-only")
	// ErrMountCheck: a mapped root is not the network mount it should be.
	ErrMountCheck = errors.New("fp-worker: mount check failed")
	// ErrParity: the calibration files did not reproduce byte for byte.
	ErrParity = errors.New("fp-worker: parity gate failed")
	// ErrToolsNotAllowed: the server does not allowlist this worker's
	// pipeline or tool pair (HTTP 409, or a mismatch seen in hello).
	ErrToolsNotAllowed = errors.New("fp-worker: pipeline or tool versions not allowlisted by the server")
	// ErrAuth: the server refused the API key (401/403).
	ErrAuth = errors.New("fp-worker: the server refused the API key")
	// ErrAPIUnavailable: the worker API answered 404 (the feature flag
	// fingerprint_remote_workers_enabled is off, or the URL is wrong).
	ErrAPIUnavailable = errors.New("fp-worker: worker API not found (fingerprint_remote_workers_enabled off, or wrong --server)")
	// ErrConfig: invalid configuration.
	ErrConfig = errors.New("fp-worker: invalid configuration")
)

// MountInfo is what statfs says about the filesystem holding a root.
type MountInfo struct {
	FSType string
	// MountPoint is f_mntonname (darwin); empty where statfs does not
	// report it (linux).
	MountPoint string
	ReadOnly   bool
}

// WindowCutter cuts one window; fingerprint.WindowTools.FileWindow in
// production.
type WindowCutter func(ctx context.Context, path string, spec fingerprint.WindowSpec) (*fingerprint.WindowPrint, error)

// Config configures Run.
type Config struct {
	// ServerURL is the server's base URL ("https://host:8484"). Plain http
	// is accepted only for a loopback host: the API key is a bearer token.
	ServerURL string
	// APIKey is the bearer key (see LoadAPIKey). Never logged.
	APIKey string
	// WorkerID names this worker to the server; 1-128 printable ASCII.
	WorkerID string
	// Roots maps a server root name ("libroot") to this host's mount of it.
	Roots map[string]string
	// Concurrency bounds the jobs cut at once (DefaultConcurrency if <= 0).
	Concurrency int
	// MaxJobs is the lease size asked for (the server clamps it; 0 asks for
	// the server's maximum).
	MaxJobs int
	// Versions is this host's (fpcalc, ffmpeg) pair, from
	// fingerprint.ToolVersions.
	Versions fingerprint.ToolVersionInfo
	// Cut cuts one window.
	Cut WindowCutter
	// HTTPClient talks to the server; nil uses a client with a 2-minute
	// per-request timeout.
	HTTPClient *http.Client
	// AllowedFSTypes are the filesystem types a root may live on (compared
	// case-insensitively); empty means {"nfs"}.
	AllowedFSTypes []string

	// Test seams; zero values mean the real thing.
	statMount    func(string) (MountInfo, error)
	writeProbe   func(string) error
	backoffMin   time.Duration
	backoffMax   time.Duration
	renewEvery   time.Duration
	flushEvery   time.Duration
	recheckEvery time.Duration
	retryDelay   time.Duration
	joinRoot     func(root, rel string) (string, error)
	openFile     func(string) (*os.File, error)
}

// maxWorkerIDLen matches the server's bound.
const maxWorkerIDLen = 128

// ValidWorkerID applies the server's rule: 1-128 printable ASCII, no space.
func ValidWorkerID(id string) bool {
	if id == "" || len(id) > maxWorkerIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}

// DefaultWorkerID is the hostname, with anything the server would refuse
// replaced by '-'.
func DefaultWorkerID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "fp-worker"
	}
	b := []byte(h)
	for i, c := range b {
		if c < 0x21 || c > 0x7e {
			b[i] = '-'
		}
	}
	if len(b) > maxWorkerIDLen {
		b = b[:maxWorkerIDLen]
	}
	return string(b)
}

// LoadAPIKey reads the API key from keyFile, or from the KeyEnvVar
// environment variable (via getenv) when keyFile is empty. A key file must be
// a regular file with no group or other permission bits (0600 or 0400) and
// hold exactly one line; anything else is refused before it is read. Errors
// never contain the key.
func LoadAPIKey(keyFile string, getenv func(string) string) (string, error) {
	if keyFile == "" {
		k := strings.TrimSpace(getenv(KeyEnvVar))
		if k == "" {
			return "", fmt.Errorf("%w: no API key: pass --key-file (a 0600 file) or set %s", ErrConfig, KeyEnvVar)
		}
		if strings.ContainsAny(k, "\r\n") {
			return "", fmt.Errorf("%w: %s holds more than one line", ErrConfig, KeyEnvVar)
		}
		return k, nil
	}
	f, err := os.Open(keyFile)
	if err != nil {
		return "", fmt.Errorf("%w: key file: %w", ErrConfig, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("%w: key file: %w", ErrConfig, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: key file %s is not a regular file", ErrConfig, keyFile)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("%w: key file %s has mode %#o; it must not be readable by group or others (chmod 600)", ErrConfig, keyFile, perm)
	}
	buf, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", fmt.Errorf("%w: key file %s: read failed", ErrConfig, keyFile)
	}
	if len(buf) > 4096 {
		return "", fmt.Errorf("%w: key file %s is too large to be an API key", ErrConfig, keyFile)
	}
	k := strings.TrimSpace(string(buf))
	if k == "" {
		return "", fmt.Errorf("%w: key file %s is empty", ErrConfig, keyFile)
	}
	if strings.ContainsAny(k, "\r\n") {
		return "", fmt.Errorf("%w: key file %s must hold only the key, on one line", ErrConfig, keyFile)
	}
	return k, nil
}

// ParseRoots parses "name=/local/path" entries into a root map. Each path
// must be absolute and each name given once.
func ParseRoots(entries []string) (map[string]string, error) {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		name, p, ok := strings.Cut(strings.TrimSpace(e), "=")
		name, p = strings.TrimSpace(name), strings.TrimSpace(p)
		if !ok || name == "" || p == "" {
			return nil, fmt.Errorf("%w: --root %q: want name=/local/path", ErrConfig, e)
		}
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("%w: --root %q: the path must be absolute", ErrConfig, e)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%w: --root %q: root %q given twice", ErrConfig, e, name)
		}
		out[name] = filepath.Clean(p)
	}
	return out, nil
}

// SplitRootsEnv splits RootsEnvVar's comma-separated value.
func SplitRootsEnv(v string) []string {
	var out []string
	for s := range strings.SplitSeq(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// checkServerURL refuses anything but https, except http to a loopback host.
func checkServerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: --server %q is not an absolute URL", ErrConfig, raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("%w: --server %q: plain http would send the API key in clear text; use https", ErrConfig, raw)
		}
	default:
		return nil, fmt.Errorf("%w: --server %q: scheme must be https", ErrConfig, raw)
	}
	return u, nil
}

func (c *Config) applyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if len(c.AllowedFSTypes) == 0 {
		c.AllowedFSTypes = []string{"nfs"}
	}
	if c.statMount == nil {
		c.statMount = statMount
	}
	if c.writeProbe == nil {
		c.writeProbe = writeProbe
	}
	if c.backoffMin <= 0 {
		c.backoffMin = 5 * time.Second
	}
	if c.backoffMax <= 0 {
		c.backoffMax = 2 * time.Minute
	}
	if c.flushEvery <= 0 {
		c.flushEvery = 30 * time.Second
	}
	if c.recheckEvery <= 0 {
		c.recheckEvery = 15 * time.Minute
	}
	if c.joinRoot == nil {
		c.joinRoot = pathutil.JoinRoot
	}
	if c.openFile == nil {
		c.openFile = os.Open
	}
	if c.retryDelay <= 0 {
		c.retryDelay = 2 * time.Second
	}
}

func (c *Config) validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("%w: no API key", ErrConfig)
	}
	if !ValidWorkerID(c.WorkerID) {
		return fmt.Errorf("%w: worker id must be 1-%d printable ASCII characters without spaces", ErrConfig, maxWorkerIDLen)
	}
	if len(c.Roots) == 0 {
		return fmt.Errorf("%w: no --root given (e.g. --root libroot=/Volumes/library)", ErrConfig)
	}
	if c.Cut == nil {
		return fmt.Errorf("%w: no window cutter", ErrConfig)
	}
	if c.Versions.Fpcalc == "" || c.Versions.FFmpeg == "" {
		return fmt.Errorf("%w: fpcalc and ffmpeg versions are required", ErrConfig)
	}
	return nil
}
