// file: core/library/root.go
// version: 1.0.0
// guid: d4890dba-7685-4e14-8535-91e4d166edd9
// last-edited: 2026-11-11

package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/falkcorp/mediacore/storage"
)

// RootID is a root's stable ID, [a-z0-9-]{1,40}: "books-library", "import-3",
// "dl-qbit-1".
type RootID string

// Valid reports whether id matches [a-z0-9-]{1,40}.
func (id RootID) Valid() bool {
	if len(id) == 0 || len(id) > 40 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// CoreOwner is the owner of core's own roots (download roots) and the caller
// identity core subsystems act as. A module's Scope binds its module ID, never
// this.
const CoreOwner storage.Owner = "core"

// Role is what a root is for.
type Role uint8

const (
	RoleInvalid  Role = iota
	RoleLibrary       // organized media; written by its owner module only
	RoleImport        // watched source folders the owner ingests from
	RoleDownload      // a download client's completed area; core-owned; modules may read and move OUT
	RoleExternal      // someone else's tree (an iTunes library): read-only, never mutated
)

func roleNames() [5]string {
	return [...]string{RoleLibrary: "library", RoleImport: "import", RoleDownload: "download", RoleExternal: "external"}
}

// String names the role ("library", "import", "download", "external"), or
// "invalid".
func (r Role) String() string {
	if r > RoleInvalid && int(r) < len(roleNames()) {
		return roleNames()[r]
	}
	return "invalid"
}

// MarshalText implements encoding.TextMarshaler. An invalid role cannot be
// written.
func (r Role) MarshalText() ([]byte, error) {
	if r == RoleInvalid || int(r) >= len(roleNames()) {
		return nil, fmt.Errorf("library: invalid root role %d", r)
	}
	return []byte(r.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (r *Role) UnmarshalText(b []byte) error {
	for i := RoleLibrary; int(i) < len(roleNames()); i++ {
		if string(b) == roleNames()[i] {
			*r = i
			return nil
		}
	}
	return fmt.Errorf("library: unknown root role %q", b)
}

// WatchMode says how a root's changes are observed.
type WatchMode uint8

const (
	WatchInvalid WatchMode = iota
	WatchAuto              // notify for local filesystems, poll for network ones
	WatchNotify
	WatchPoll
	WatchOff
)

func watchNames() [5]string {
	return [...]string{WatchAuto: "auto", WatchNotify: "notify", WatchPoll: "poll", WatchOff: "off"}
}

// String names the mode, or "invalid".
func (w WatchMode) String() string {
	if w > WatchInvalid && int(w) < len(watchNames()) {
		return watchNames()[w]
	}
	return "invalid"
}

// MarshalText implements encoding.TextMarshaler.
func (w WatchMode) MarshalText() ([]byte, error) {
	if w == WatchInvalid || int(w) >= len(watchNames()) {
		return nil, fmt.Errorf("library: invalid watch mode %d", w)
	}
	return []byte(w.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (w *WatchMode) UnmarshalText(b []byte) error {
	for i := WatchAuto; int(i) < len(watchNames()); i++ {
		if string(b) == watchNames()[i] {
			*w = i
			return nil
		}
	}
	return fmt.Errorf("library: unknown watch mode %q", b)
}

// SentinelMode says how strictly a root's sentinel decides "mounted".
type SentinelMode uint8

const (
	SentinelInvalid SentinelMode = iota
	// SentinelRequired is the mode of new roots: <AppDir>/root.json must exist
	// and name this install and root ID.
	SentinelRequired
	// SentinelBootstrap is the mode of roots migrated from ABO (05-library
	// 5L.10): the sentinel is created when the bootstrap heuristic passes, a
	// present sentinel must match, and an absent one leaves the decision to the
	// heuristic.
	SentinelBootstrap
)

func sentinelNames() [3]string {
	return [...]string{SentinelRequired: "required", SentinelBootstrap: "bootstrap"}
}

// String names the mode, or "invalid".
func (s SentinelMode) String() string {
	if s > SentinelInvalid && int(s) < len(sentinelNames()) {
		return sentinelNames()[s]
	}
	return "invalid"
}

// MarshalText implements encoding.TextMarshaler.
func (s SentinelMode) MarshalText() ([]byte, error) {
	if s == SentinelInvalid || int(s) >= len(sentinelNames()) {
		return nil, fmt.Errorf("library: invalid sentinel mode %d", s)
	}
	return []byte(s.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *SentinelMode) UnmarshalText(b []byte) error {
	for i := SentinelRequired; int(i) < len(sentinelNames()); i++ {
		if string(b) == sentinelNames()[i] {
			*s = i
			return nil
		}
	}
	return fmt.Errorf("library: unknown sentinel mode %q", b)
}

// Alias is a display rendering of a root for remote clients (ABO's PathAlias).
// It is never used for I/O.
type Alias struct {
	Windows string `json:"windows,omitempty"`
	UNC     string `json:"unc,omitempty"`
	SMBURL  string `json:"smb_url,omitempty"`
}

// The defaults RootSpec takes at registration for the fields it leaves empty.
const (
	DefaultAppDir     = ".mediacore"
	DefaultQuarantine = ".mediacore/quarantine"
)

// RootSpec is one root: the element type of the core.library.roots config
// entry list (05-library 5L.3.1, 5L.7). Its JSON form is the stored one.
type RootSpec struct {
	ID         RootID        `json:"id"`
	Name       string        `json:"name,omitempty"`
	Path       string        `json:"path"` // absolute host path; resolved once (EvalSymlinks) at registration
	Role       Role          `json:"role"`
	Owner      storage.Owner `json:"owner"` // module ID or "core"
	Watch      WatchMode     `json:"watch,omitempty"`
	Sentinel   SentinelMode  `json:"sentinel,omitempty"`
	MinFree    int64         `json:"min_free"`             // bytes a transfer into this root must leave free; MinFreeDefault = core.library.free_space.default_min_bytes
	Quarantine string        `json:"quarantine,omitempty"` // rel dir; default DefaultQuarantine; books keeps ".failed"
	AppDir     string        `json:"app_dir,omitempty"`    // rel dir for app state; default DefaultAppDir
	Aliases    []Alias       `json:"aliases,omitempty"`    // display renderings for remote clients; never used for I/O
}

// MinFreeDefault is the MinFree that takes core.library.free_space.default_min_bytes
// as the root's floor. A stored entry that omits min_free decodes to it; an
// explicit 0 is "no floor" (the migrated books root, which HEAD never gave one).
const MinFreeDefault int64 = -1

// UnmarshalJSON decodes a RootSpec, giving an omitted min_free the MinFreeDefault
// meaning.
func (s *RootSpec) UnmarshalJSON(b []byte) error {
	type plain RootSpec
	aux := struct {
		*plain
		MinFree *int64 `json:"min_free"`
	}{plain: (*plain)(s)}
	*s = RootSpec{}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	s.MinFree = MinFreeDefault
	if aux.MinFree != nil {
		s.MinFree = *aux.MinFree
	}
	return nil
}

func isAbs(p string) bool { return filepath.IsAbs(p) }

// ErrBadRootSpec is returned for a RootSpec that cannot be registered.
var ErrBadRootSpec = errors.New("library: invalid root spec")

// normalized returns s with the defaults applied: Watch auto, Sentinel
// required, and the default AppDir and Quarantine.
func (s RootSpec) normalized() RootSpec {
	if s.Watch == WatchInvalid {
		s.Watch = WatchAuto
	}
	if s.Sentinel == SentinelInvalid {
		s.Sentinel = SentinelRequired
	}
	if s.AppDir == "" {
		s.AppDir = DefaultAppDir
	}
	if s.Quarantine == "" {
		s.Quarantine = DefaultQuarantine
	}
	if s.Name == "" {
		s.Name = string(s.ID)
	}
	s.Aliases = append([]Alias(nil), s.Aliases...)
	return s
}

// validate checks a normalized spec's own fields (not its path on disk).
func (s RootSpec) validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: root %q: %s", ErrBadRootSpec, s.ID, fmt.Sprintf(format, args...))
	}
	switch {
	case !s.ID.Valid():
		return fmt.Errorf("%w: id %q is not [a-z0-9-]{1,40}", ErrBadRootSpec, s.ID)
	case s.Role < RoleLibrary || s.Role > RoleExternal:
		return bad("role is not set")
	case s.Owner == "":
		return bad("owner is not set")
	case s.Role == RoleDownload && s.Owner != CoreOwner:
		return bad("a download root is core-owned")
	case s.Path == "" || !isAbs(s.Path):
		return bad("path %q is not absolute", s.Path)
	case s.MinFree < MinFreeDefault:
		return bad("min_free is negative")
	case s.Watch > WatchOff:
		return bad("watch mode %d", s.Watch)
	case s.Sentinel > SentinelBootstrap:
		return bad("sentinel mode %d", s.Sentinel)
	}
	for _, f := range []struct{ name, rel string }{{"app_dir", s.AppDir}, {"quarantine", s.Quarantine}} {
		if err := ValidateRel(f.rel); err != nil {
			return bad("%s: %v", f.name, err)
		}
	}
	return nil
}

// Path is the only way modules name a file. JSON:
// {"root":"books-library","rel":"Author/Title/01.m4b"}. Rel is POSIX-relative,
// clean, never "..", never absolute, NUL-free and byte-exact (ValidateRel); the
// empty Rel names the root directory itself, which no transfer accepts.
type Path struct {
	Root RootID `json:"root"`
	Rel  string `json:"rel"`
}

// ErrBadRel reports a Path or relative name that is not a valid root-relative
// path.
var ErrBadRel = errors.New("library: invalid root-relative path")

// ValidateRel checks a root-relative wire path: non-empty, POSIX-relative (no
// leading "/"), already clean (no ".", ".." or doubled or trailing slash),
// never ".." or under "../", and free of NUL. It does not require UTF-8 and
// does not normalize Unicode: an NFC and an NFD spelling are two different
// files and each stays itself (pathutil.ValidateRel semantics).
func ValidateRel(rel string) error {
	switch {
	case rel == "":
		return fmt.Errorf("%w: empty", ErrBadRel)
	case strings.IndexByte(rel, 0) >= 0:
		return fmt.Errorf("%w: contains NUL", ErrBadRel)
	case strings.HasPrefix(rel, "/"):
		return fmt.Errorf("%w: absolute", ErrBadRel)
	case rel == ".." || strings.HasPrefix(rel, "../"):
		return fmt.Errorf("%w: escapes the root", ErrBadRel)
	case path.Clean(rel) != rel || rel == ".":
		return fmt.Errorf("%w: not clean", ErrBadRel)
	}
	return nil
}

// Validate checks p: a valid root ID and either the empty Rel (the root
// directory) or a ValidateRel path.
func (p Path) Validate() error {
	if !p.Root.Valid() {
		return fmt.Errorf("%w: root %q", ErrBadRel, p.Root)
	}
	if p.Rel == "" {
		return nil
	}
	return ValidateRel(p.Rel)
}

// IsRoot reports whether p names the root directory itself.
func (p Path) IsRoot() bool { return p.Rel == "" }

// Join appends elem to p. Each element must itself be a valid relative path (a
// single name or a clean sub-path): Join never cleans, so a ".." element is an
// error and not a silent step up.
func (p Path) Join(elem ...string) (Path, error) {
	rel := p.Rel
	for _, e := range elem {
		if err := ValidateRel(e); err != nil {
			return Path{}, err
		}
		if rel == "" {
			rel = e
		} else {
			rel += "/" + e
		}
	}
	return Path{Root: p.Root, Rel: rel}, nil
}

// Dir is p's parent directory in the same root; the root itself for a
// top-level name.
func (p Path) Dir() Path {
	i := strings.LastIndexByte(p.Rel, '/')
	if i < 0 {
		return Path{Root: p.Root}
	}
	return Path{Root: p.Root, Rel: p.Rel[:i]}
}

// Base is the last element of p's Rel; "" for the root directory.
func (p Path) Base() string {
	if i := strings.LastIndexByte(p.Rel, '/'); i >= 0 {
		return p.Rel[i+1:]
	}
	return p.Rel
}

// String is "root:rel", for logs.
func (p Path) String() string { return string(p.Root) + ":" + p.Rel }

// RootFilter selects roots. Owner, when set, keeps the roots that owner owns
// (or, with Readable, may read); Role, when set, keeps that role; Readable
// without an Owner keeps every root.
type RootFilter struct {
	Owner    storage.Owner
	Role     Role
	Readable bool
}

// Usage is a filesystem's capacity. Avail is Bavail, the space available to an
// unprivileged process, not Bfree.
type Usage struct{ Total, Avail uint64 }
